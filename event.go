// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

import (
	"fmt"
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
	provider       string
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
	var ts time.Time
	if record.Timestamp() != 0 {
		ts = record.Timestamp().AsTime()
	} else if record.ObservedTimestamp() != 0 {
		ts = record.ObservedTimestamp().AsTime()
	} else {
		ts = time.Now()
		attrs["coding_agent.timestamp.inferred"] = true
	}
	return agentEvent{
		name: name, provider: "codex", conversationID: conversationID,
		timestamp: ts, attrs: attrs, resource: resource.Attributes().AsRaw(),
	}, true
}

func stringValue(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	case nil:
		return ""
	default:
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
		if value <= uint64(^uint64(0)>>1) {
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
