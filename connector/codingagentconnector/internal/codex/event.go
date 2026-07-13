// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"crypto/sha256"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

const (
	attrEventName      = "event.name"
	attrConversationID = "conversation.id"
)

type agentEvent struct {
	name           string
	conversationID string
	timestamp      time.Time
	attrs          map[string]any
	resource       map[string]any
}

func parseEvent(record plog.LogRecord, resource pcommon.Resource) (agentEvent, bool) {
	attrs := record.Attributes().AsRaw()
	name := stringValue(attrs[attrEventName])
	if name == "" && record.Body().Type() == pcommon.ValueTypeMap {
		body := record.Body().Map().AsRaw()
		name = stringValue(body[attrEventName])
		for k, v := range body {
			if _, exists := attrs[k]; !exists {
				attrs[k] = v
			}
		}
	}
	if !strings.HasPrefix(name, "codex.") {
		return agentEvent{}, false
	}
	conversationID := stringValue(attrs[attrConversationID])
	if conversationID == "" {
		return agentEvent{}, false
	}
	stripContent(attrs)
	var ts time.Time
	if record.Timestamp() != 0 {
		ts = record.Timestamp().AsTime()
	} else {
		if value := stringValue(attrs["event.timestamp"]); value != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
				ts = parsed
			}
		}
		if ts.IsZero() && record.ObservedTimestamp() != 0 {
			ts = record.ObservedTimestamp().AsTime()
		}
	}
	if ts.IsZero() {
		ts = time.Now()
		attrs["coding_agent.timestamp.inferred"] = true
	}
	return agentEvent{
		name: name, conversationID: conversationID,
		timestamp: ts, attrs: attrs, resource: resource.Attributes().AsRaw(),
	}, true
}

// fingerprint returns a stable content hash used to deduplicate redelivered
// events within a live turn. OTLP delivery is at-least-once, so without it a
// resent batch would double-count token usage and duplicate spans.
func (e agentEvent) fingerprint() [sha256.Size]byte {
	keys := make([]string, 0, len(e.attrs))
	for key := range e.attrs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	h := sha256.New()
	_, _ = h.Write([]byte(e.name))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.FormatInt(e.timestamp.UnixNano(), 10)))
	for _, key := range keys {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(key))
		_, _ = h.Write([]byte{'='})
		_, _ = h.Write([]byte(stringValue(e.attrs[key])))
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

func stripContent(attrs map[string]any) {
	for _, key := range []string{"prompt", "arguments", "output"} {
		delete(attrs, key)
	}
}

func stringValue(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case nil:
		return ""
	default:
		// fmt.Sprint invokes String() for fmt.Stringer values, so no dedicated
		// case is needed for them.
		return fmt.Sprint(value)
	}
}

func int64Value(v any) (int64, bool) {
	switch value := v.(type) {
	case int:
		return int64(value), true
	case int32:
		return int64(value), true
	case int64:
		return value, true
	case uint64:
		if value <= math.MaxInt64 {
			return int64(value), true
		}
	case float64:
		return int64(value), true
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		return parsed, err == nil
	}
	return 0, false
}

func boolValue(v any) (bool, bool) {
	switch value := v.(type) {
	case bool:
		return value, true
	case string:
		parsed, err := strconv.ParseBool(value)
		return parsed, err == nil
	}
	return false, false
}
