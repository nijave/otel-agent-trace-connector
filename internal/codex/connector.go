// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"context"
	"crypto/sha256"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/otel/metric"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
	"go.uber.org/zap"
)

type turnKey struct{ provider, conversationID string }

type turnState struct {
	key          turnKey
	events       []agentEvent
	seen         map[[sha256.Size]byte]struct{}
	resource     map[string]any
	first        time.Time
	last         time.Time
	lastSeen     time.Time
	completeSeen bool
	completionAt time.Time
	promptSeen   bool
	truncated    bool
}

type codingAgentConnector struct {
	config       *Config
	set          connector.Settings
	next         consumer.Traces
	scopeVersion string

	mu        sync.Mutex
	turns     map[turnKey]*turnState
	stop      chan struct{}
	done      chan struct{}
	started   atomic.Bool
	startOnce sync.Once
	stopOnce  sync.Once

	telemetry *telemetry
	metricReg metric.Registration
}

func newConnector(cfg *Config, set connector.Settings, next consumer.Traces) (*codingAgentConnector, error) {
	meterProvider := set.MeterProvider
	if meterProvider == nil {
		meterProvider = noopmetric.NewMeterProvider()
	}
	meter := meterProvider.Meter(instrumentationScope)
	tel, err := newTelemetry(meter)
	if err != nil {
		return nil, err
	}
	scopeVersion := set.BuildInfo.Version
	if scopeVersion == "" {
		scopeVersion = defaultScopeVersion
	}
	c := &codingAgentConnector{
		config: cfg, set: set, next: next, scopeVersion: scopeVersion,
		turns: make(map[turnKey]*turnState),
		stop:  make(chan struct{}), done: make(chan struct{}),
		telemetry: tel,
	}
	// Report active-turn count on demand so operators can watch state approach
	// max_active_turns without the connector maintaining a hand-balanced counter.
	reg, err := meter.RegisterCallback(func(_ context.Context, observer metric.Observer) error {
		observer.ObserveInt64(tel.activeTurns, c.activeTurnCount())
		return nil
	}, tel.activeTurns)
	if err != nil {
		return nil, err
	}
	c.metricReg = reg
	return c, nil
}

// New creates the stateful Codex logs-to-traces edge.
func New(cfg *Config, set connector.Settings, next consumer.Traces) (connector.Logs, error) {
	return newConnector(cfg, set, next)
}

func (c *codingAgentConnector) activeTurnCount() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return int64(len(c.turns))
}

func (*codingAgentConnector) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (c *codingAgentConnector) Start(context.Context, component.Host) error {
	c.startOnce.Do(func() {
		c.started.Store(true)
		go c.sweepLoop()
	})
	return nil
}

func (c *codingAgentConnector) Shutdown(ctx context.Context) error {
	if c.metricReg != nil {
		_ = c.metricReg.Unregister()
	}
	c.stopOnce.Do(func() { close(c.stop) })
	// Only wait for the sweep loop if it was ever started; otherwise done never
	// closes and Shutdown would block until the context deadline.
	if c.started.Load() {
		select {
		case <-c.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return c.flushAll(ctx, "shutdown")
}

func (c *codingAgentConnector) ConsumeLogs(ctx context.Context, logs plog.Logs) error {
	events := make([]agentEvent, 0, logs.LogRecordCount())
	for i := 0; i < logs.ResourceLogs().Len(); i++ {
		rl := logs.ResourceLogs().At(i)
		for j := 0; j < rl.ScopeLogs().Len(); j++ {
			records := rl.ScopeLogs().At(j).LogRecords()
			for k := 0; k < records.Len(); k++ {
				if event, ok := parseEvent(records.At(k), rl.Resource()); ok {
					events = append(events, event)
				}
			}
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].timestamp.Before(events[j].timestamp) })
	var ready []*turnState
	var reasons []string
	var dropped int64
	now := time.Now()
	c.mu.Lock()
	for _, event := range events {
		key := turnKey{provider: event.provider, conversationID: event.conversationID}
		turn := c.turns[key]
		if turn != nil && turn.seenEvent(event) {
			// Redelivered event: skip before the prompt check so a resent
			// prompt does not falsely supersede its own turn.
			dropped++
			continue
		}
		if event.name == "codex.user_prompt" && turn != nil && turn.promptSeen {
			delete(c.turns, key)
			ready = append(ready, turn)
			reasons = append(reasons, "superseded")
			turn = nil
		}
		if turn == nil {
			if len(c.turns) >= c.config.MaxActiveTurns {
				ready = append(ready, c.evictOldestLocked())
				reasons = append(reasons, "evicted")
			}
			turn = &turnState{key: key, first: event.timestamp, last: event.timestamp, lastSeen: now, resource: event.resource}
			c.turns[key] = turn
		}
		turn.add(event, now, c.config.MaxEvents)
	}
	c.mu.Unlock()
	c.telemetry.recordDroppedEvents(ctx, dropped)
	return c.emit(ctx, ready, reasons)
}

func (t *turnState) seenEvent(event agentEvent) bool {
	_, ok := t.seen[event.fingerprint()]
	return ok
}

func (t *turnState) add(event agentEvent, now time.Time, maxEvents int) {
	fingerprint := event.fingerprint()
	if _, duplicate := t.seen[fingerprint]; duplicate {
		return
	}
	if event.timestamp.Before(t.first) {
		t.first = event.timestamp
	}
	if event.timestamp.After(t.last) {
		t.last = event.timestamp
	}
	t.lastSeen = now
	if event.name == "codex.user_prompt" {
		t.promptSeen = true
	}
	if event.name == "codex.sse_event" && stringValue(event.attrs["event.kind"]) == "response.completed" {
		if !event.timestamp.Before(t.completionAt) {
			t.completeSeen = true
			t.completionAt = event.timestamp
		}
	} else if t.completeSeen && !event.timestamp.Before(t.completionAt) && continuesTurn(event.name) {
		t.completeSeen = false
	}
	if len(t.events) < maxEvents {
		t.events = append(t.events, event)
		if t.seen == nil {
			t.seen = make(map[[sha256.Size]byte]struct{})
		}
		t.seen[fingerprint] = struct{}{}
	} else {
		t.truncated = true
	}
}

func continuesTurn(eventName string) bool {
	return eventName == "codex.tool_result" || eventName == "codex.api_request" || eventName == "codex.websocket_request"
}

func (c *codingAgentConnector) sweepLoop() {
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
			ready, reasons := c.collectReady(now)
			for i, turn := range ready {
				if err := c.next.ConsumeTraces(context.Background(), buildTrace(turn, reasons[i], c.scopeVersion)); err != nil {
					c.set.Logger.Error("failed to emit reconstructed coding-agent trace", zap.Error(err))
				}
				c.telemetry.recordEmitted(context.Background(), reasons[i], turn.truncated)
			}
		case <-c.stop:
			return
		}
	}
}

func (c *codingAgentConnector) collectReady(now time.Time) ([]*turnState, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var turns []*turnState
	var reasons []string
	for key, turn := range c.turns {
		reason := ""
		if turn.completeSeen && now.Sub(turn.lastSeen) >= c.config.ReorderWindow {
			reason = "completed"
		} else if now.Sub(turn.lastSeen) >= c.config.TurnTimeout {
			reason = "timeout"
		}
		if reason != "" {
			delete(c.turns, key)
			turns = append(turns, turn)
			reasons = append(reasons, reason)
		}
	}
	return turns, reasons
}

func (c *codingAgentConnector) evictOldestLocked() *turnState {
	var oldestKey turnKey
	var oldest *turnState
	for key, turn := range c.turns {
		if oldest == nil || turn.lastSeen.Before(oldest.lastSeen) {
			oldestKey, oldest = key, turn
		}
	}
	delete(c.turns, oldestKey)
	return oldest
}

func (c *codingAgentConnector) emit(ctx context.Context, turns []*turnState, reasons []string) error {
	// Continue past a failing turn so one transient downstream error during a
	// drain does not abandon the turns already removed from active state.
	var errs error
	for i, turn := range turns {
		if turn == nil {
			continue
		}
		if err := c.next.ConsumeTraces(ctx, buildTrace(turn, reasons[i], c.scopeVersion)); err != nil {
			errs = errors.Join(errs, err)
		}
		c.telemetry.recordEmitted(ctx, reasons[i], turn.truncated)
	}
	return errs
}

func (c *codingAgentConnector) flushAll(ctx context.Context, reason string) error {
	c.mu.Lock()
	turns := make([]*turnState, 0, len(c.turns))
	reasons := make([]string, 0, len(c.turns))
	for key, turn := range c.turns {
		turns = append(turns, turn)
		reasons = append(reasons, reason)
		delete(c.turns, key)
	}
	c.mu.Unlock()
	return c.emit(ctx, turns, reasons)
}

var _ connector.Logs = (*codingAgentConnector)(nil)
