// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

const (
	// cursorScopePrefix claims records by instrumentation scope. The wire
	// reference documents scope `cursor.telemetry`/0.1.0`; prefix matching
	// tolerates version-suffixed or renamed-future scope names.
	cursorScopePrefix = "cursor.telemetry"

	attrEventID      = "cursor.event.id"
	attrConversation = "cursor.conversation.id"
	attrUsageEventID = "cursor.usage_event.id"

	// BodyAPIRequest and friends name the OTLP log-record bodies from the
	// wire reference. Bodies are unprefixed ("api_request", not
	// "cursor.api.request"); the cursor.* namespace carries attributes only.
	BodyAPIRequest            = "api_request"
	BodyAPIError              = "api_error"
	BodySkillActivated        = "skill_activated"
	BodyHookExecutionComplete = "hook_execution_complete"
)

// Event is one claimed Cursor log record with the correlation keys the wire
// guarantees or the connector requires. Attrs carries the raw record
// attributes; span construction copies from it by allowlist only.
type Event struct {
	Body           string
	EventID        string
	ConversationID string
	UsageEventID   string
	Timestamp      time.Time
	Attrs          map[string]any
	Resource       map[string]any
}

// IsCorrectionBody reports whether a body names an api.correction record
// ("api_correction_<kind>" per the wire reference).
func IsCorrectionBody(body string) bool {
	return strings.HasPrefix(body, "api_correction_")
}

// IsCloudAgentBody reports whether a body belongs to the cloud_agents family.
func IsCloudAgentBody(body string) bool {
	return strings.HasPrefix(body, "cloud_agent_")
}

// ParseRecord claims a record for the Cursor edge when its instrumentation
// scope matches and it carries a conversation id. It declines codex-named
// records so the two logs edges stay disjoint whatever a payload contains.
func ParseRecord(record plog.LogRecord, scopeName string, resource pcommon.Resource) (Event, bool) {
	if !strings.HasPrefix(scopeName, cursorScopePrefix) {
		return Event{}, false
	}
	attrs := record.Attributes().AsRaw()
	if strings.HasPrefix(StringValue(attrs["event.name"]), "codex.") {
		return Event{}, false
	}
	conversationID := StringValue(attrs[attrConversation])
	if conversationID == "" {
		return Event{}, false
	}

	var body string
	if record.Body().Type() == pcommon.ValueTypeStr {
		body = record.Body().Str()
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

	return Event{
		Body:           body,
		EventID:        eventID(body, ts, attrs),
		ConversationID: conversationID,
		UsageEventID:   StringValue(attrs[attrUsageEventID]),
		Timestamp:      ts,
		Attrs:          attrs,
		Resource:       resource.Attributes().AsRaw(),
	}, true
}

// eventID returns the wire dedupe key, or derives a stable one for malformed
// records missing it: dedupe on the raw empty string would collapse every
// id-less record into one another.
func eventID(body string, ts time.Time, attrs map[string]any) string {
	if id := StringValue(attrs[attrEventID]); id != "" {
		return id
	}
	keys := make([]string, 0, len(attrs))
	for key := range attrs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	h := sha256.New()
	_, _ = h.Write([]byte(body))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.FormatInt(ts.UnixNano(), 10)))
	for _, key := range keys {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(key))
		_, _ = h.Write([]byte{'='})
		_, _ = h.Write([]byte(StringValue(attrs[key])))
	}
	return fmt.Sprintf("derived:%x", h.Sum(nil))
}

func StringValue(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}

func Int64Value(v any) (int64, bool) {
	switch value := v.(type) {
	case int:
		return int64(value), true
	case int32:
		return int64(value), true
	case int64:
		return value, true
	case uint64:
		if value <= 1<<62 {
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

func BoolValue(v any) (bool, bool) {
	switch value := v.(type) {
	case bool:
		return value, true
	case string:
		parsed, err := strconv.ParseBool(value)
		return parsed, err == nil
	}
	return false, false
}
