// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
	"go.uber.org/zap"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/codex"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/metadata"
)

// finalizedBurst pairs a burst removed from active state with the reason it
// closed, so the emit path carries them together.
type finalizedBurst struct {
	burst  *burstState
	reason string
}

type burstState struct {
	conversationID string
	events         []Event
	seen           map[string]struct{}
	resource       map[string]any
	first          time.Time
	last           time.Time
	lastSeen       time.Time
	truncated      bool
}

type cursorConnector struct {
	config       *codex.Config
	set          connector.Settings
	next         consumer.Traces
	scopeVersion string

	mu     sync.Mutex
	bursts map[string]*burstState
	stop   chan struct{}
	done   chan struct{}

	started   atomic.Bool
	startOnce sync.Once
	stopOnce  sync.Once

	telemetry *telemetry
}

// New creates the stateful Cursor logs-to-traces edge. It shares the
// provider-neutral Config alias with the codex edge.
func New(cfg *codex.Config, set connector.Settings, next consumer.Traces) (connector.Logs, error) {
	ts := set.TelemetrySettings
	if ts.MeterProvider == nil {
		ts.MeterProvider = noopmetric.NewMeterProvider()
	}
	builder, err := metadata.NewTelemetryBuilder(ts)
	if err != nil {
		return nil, err
	}
	scopeVersion := set.BuildInfo.Version
	if scopeVersion == "" {
		scopeVersion = codex.DefaultScopeVersion
	}
	c := &cursorConnector{
		config: cfg, set: set, next: next, scopeVersion: scopeVersion,
		bursts: make(map[string]*burstState),
		stop:   make(chan struct{}), done: make(chan struct{}),
		telemetry: &telemetry{builder: builder},
	}
	// The active-turns gauge is shared with the codex edge; the provider
	// attribute keeps the two async observations from colliding on one
	// timeseries.
	if err := builder.RegisterCodingAgentActiveTurnsCallback(func(_ context.Context, observer metric.Int64Observer) error {
		observer.Observe(c.activeBurstCount(), metric.WithAttributes(attribute.String("provider", "cursor")))
		return nil
	}); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *cursorConnector) activeBurstCount() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return int64(len(c.bursts))
}

func (*cursorConnector) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (c *cursorConnector) Start(context.Context, component.Host) error {
	c.startOnce.Do(func() {
		c.started.Store(true)
		go c.sweepLoop()
	})
	return nil
}

func (c *cursorConnector) Shutdown(ctx context.Context) error {
	c.telemetry.builder.Shutdown()
	c.stopOnce.Do(func() { close(c.stop) })
	if c.started.Load() {
		select {
		case <-c.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return c.flushAll(ctx, "shutdown")
}

func (c *cursorConnector) ConsumeLogs(ctx context.Context, logs plog.Logs) error {
	events := make([]Event, 0, logs.LogRecordCount())
	for i := 0; i < logs.ResourceLogs().Len(); i++ {
		rl := logs.ResourceLogs().At(i)
		for j := 0; j < rl.ScopeLogs().Len(); j++ {
			sl := rl.ScopeLogs().At(j)
			scopeName := sl.Scope().Name()
			for k := 0; k < sl.LogRecords().Len(); k++ {
				if event, ok := ParseRecord(sl.LogRecords().At(k), scopeName, rl.Resource()); ok {
					events = append(events, event)
				}
			}
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Timestamp.Before(events[j].Timestamp) })
	var finalized []finalizedBurst
	var dropped int64
	now := time.Now()
	c.mu.Lock()
	for _, event := range events {
		key := event.ConversationID
		burst := c.bursts[key]
		if burst != nil {
			if _, duplicate := burst.seen[event.EventID]; duplicate {
				dropped++
				continue
			}
		}
		if burst == nil {
			if len(c.bursts) >= c.config.MaxActiveTurns {
				finalized = append(finalized, finalizedBurst{burst: c.evictOldestLocked(), reason: "evicted"})
			}
			burst = &burstState{
				conversationID: key,
				first:          event.Timestamp,
				last:           event.Timestamp,
				lastSeen:       now,
				resource:       event.Resource,
			}
			c.bursts[key] = burst
		}
		burst.add(event, now, c.config.MaxEvents)
	}
	c.mu.Unlock()
	c.telemetry.recordDroppedRecords(ctx, dropped)
	return c.emit(ctx, finalized)
}

func (b *burstState) add(event Event, now time.Time, maxEvents int) {
	if event.Timestamp.Before(b.first) {
		b.first = event.Timestamp
	}
	if event.Timestamp.After(b.last) {
		b.last = event.Timestamp
	}
	b.lastSeen = now
	if len(b.events) < maxEvents {
		b.events = append(b.events, event)
		if b.seen == nil {
			b.seen = make(map[string]struct{})
		}
		b.seen[event.EventID] = struct{}{}
	} else {
		b.truncated = true
	}
}

func (c *cursorConnector) sweepLoop() {
	interval := c.config.ReorderWindow / 2
	if interval <= 0 {
		interval = 10 * time.Millisecond
	} else if interval > time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer func() { ticker.Stop(); close(c.done) }()
	for {
		select {
		case now := <-ticker.C:
			if err := c.emit(context.Background(), c.collectReady(now)); err != nil {
				c.set.Logger.Error("failed to emit reconstructed cursor trace", zap.Error(err))
			}
		case <-c.stop:
			return
		}
	}
}

// collectReady closes bursts on two clocks. The timeout check runs first and
// measures from the burst's first event: a burst that keeps receiving records
// never goes quiet, so turn_timeout is the only cap on its growth. The quiet
// check measures arrival silence since the last record and is the normal
// close, because the Cursor wire has no completion event.
func (c *cursorConnector) collectReady(now time.Time) []finalizedBurst {
	c.mu.Lock()
	defer c.mu.Unlock()
	var finalized []finalizedBurst
	for key, burst := range c.bursts {
		reason := ""
		if now.Sub(burst.first) >= c.config.TurnTimeout {
			reason = "timeout"
		} else if now.Sub(burst.lastSeen) >= c.config.ReorderWindow {
			reason = "quiet"
		}
		if reason != "" {
			delete(c.bursts, key)
			finalized = append(finalized, finalizedBurst{burst: burst, reason: reason})
		}
	}
	return finalized
}

func (c *cursorConnector) evictOldestLocked() *burstState {
	var oldestKey string
	var oldest *burstState
	for key, burst := range c.bursts {
		if oldest == nil || burst.lastSeen.Before(oldest.lastSeen) {
			oldestKey, oldest = key, burst
		}
	}
	delete(c.bursts, oldestKey)
	return oldest
}

func (c *cursorConnector) emit(ctx context.Context, finalized []finalizedBurst) error {
	// Continue past a failing burst so one transient downstream error during a
	// drain does not abandon the bursts already removed from active state.
	var errs error
	for _, fb := range finalized {
		if fb.burst == nil {
			continue
		}
		traces, err := buildTrace(fb.burst, fb.reason, c.scopeVersion)
		if err != nil {
			// Deliver anyway: the spans are intact even when resource
			// attributes fail to copy, and returning the error would make the
			// upstream retry rebuild the finalized conversation as a fresh
			// burst.
			c.set.Logger.Error("failed to fully reconstruct cursor trace", zap.Error(err))
		}
		if err := c.next.ConsumeTraces(ctx, traces); err != nil {
			errs = errors.Join(errs, err)
			continue
		}
		c.telemetry.recordEmitted(ctx, fb.reason, fb.burst.truncated)
	}
	return errs
}

func (c *cursorConnector) flushAll(ctx context.Context, reason string) error {
	c.mu.Lock()
	finalized := make([]finalizedBurst, 0, len(c.bursts))
	for key, burst := range c.bursts {
		finalized = append(finalized, finalizedBurst{burst: burst, reason: reason})
		delete(c.bursts, key)
	}
	c.mu.Unlock()
	return c.emit(ctx, finalized)
}

var _ connector.Logs = (*cursorConnector)(nil)
