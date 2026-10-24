// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

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
	// cursorScopePrefix claims records by instrumentation scope. The wire
	// reference documents scope `cursor.telemetry`/0.1.0`; prefix matching
	// tolerates version-suffixed or renamed-future scope names.
	cursorScopePrefix = "cursor.telemetry"

	attrEventID      = "cursor.event.id"
	attrEventName    = "event.name"
	attrConversation = "cursor.conversation.id"
	attrUsageEventID = "cursor.usage_event.id"
	// attrConversationContent marks the opt-in conversation_content family,
	// whose body is scrubbed payload text instead of a constant event name.
	// The wire sets it on every record of the family.
	attrConversationContent = "cursor.conversation.content_truncated"

	// NameAPIRequest and friends are canonical event names: the wire event
	// name flattened ("cursor.api.request" → "api_request"), which is also
	// the constant the wire puts in the body of every record outside the
	// conversation_content family.
	NameAPIRequest = "api_request"
	NameAPIError   = "api_error"
	// NameGrokBotToolResult and NameGrokBotMCPToolCall are the Grok Bot
	// action rows that record one settled tool call: builtin tools and MCP
	// tools respectively. They become execute_tool spans.
	NameGrokBotToolResult  = "grok_bot_tool_result"
	NameGrokBotMCPToolCall = "grok_bot_mcp_tool_call"
)

// kindAttrs restores the kind the wire puts in the body but not in the event
// name, so canonical event names keep the form they carried when they came
// from the body ("api_correction_<kind>", "cloud_agent_pull_request_<kind>",
// "cloud_agent_setup_<kind>"). The wire documents each as an always-present
// per-family attribute.
var kindAttrs = map[string]string{
	"cursor.api.correction":           "cursor.api.correction.kind",
	"cursor.cloud_agent.pull_request": "cursor.cloud_agent.pull_request.kind",
	"cursor.cloud_agent.setup":        "cursor.cloud_agent.setup.kind",
}

// Event is one claimed Cursor log record with the correlation keys the wire
// guarantees or the connector requires. Attrs carries the raw record
// attributes; span construction copies from it by allowlist only. Name is the
// canonical event name (see canonicalEventName), never the raw record body:
// the conversation_content family carries prompt, response, and tool I/O text
// in the body.
type Event struct {
	Name           string
	EventID        string
	ConversationID string
	UsageEventID   string
	Timestamp      time.Time
	Attrs          map[string]any
	Resource       map[string]any
}

// IsCorrectionEvent reports whether a canonical event name names an
// api.correction record ("api_correction" plus the kind, when the wire
// carries it).
func IsCorrectionEvent(name string) bool {
	return strings.HasPrefix(name, "api_correction")
}

// IsToolEvent reports whether a canonical event name is one of the Grok Bot
// tool rows the edge turns into execute_tool spans.
func IsToolEvent(name string) bool {
	return name == NameGrokBotToolResult || name == NameGrokBotMCPToolCall
}

// wireEventName reads the record's OTLP event name, falling back to the
// event.name attribute the OTel logs convention also uses.
func wireEventName(record plog.LogRecord, attrs map[string]any) string {
	if name := record.EventName(); name != "" {
		return name
	}
	return StringValue(attrs[attrEventName])
}

// flattenEventName is the canonical form of a wire event name: the cursor.
// namespace drops and dot segments join with underscores ("cursor.api.request"
// → "api_request").
func flattenEventName(eventName string) string {
	return strings.ReplaceAll(strings.TrimPrefix(eventName, "cursor."), ".", "_")
}

// isConversationContent reports whether a record belongs to the opt-in
// conversation_content family, the only family whose body is a payload rather
// than a constant event name. The wire says to route on the event name; the
// content_truncated attribute it sets on every record of the family is the
// backstop for a record that arrives without one.
func isConversationContent(eventName string, attrs map[string]any) bool {
	if _, ok := attrs[attrConversationContent]; ok {
		return true
	}
	return strings.HasPrefix(strings.TrimPrefix(eventName, "cursor."), "conversation.")
}

// canonicalEventName names a record for canonical output. The wire event name
// is the source, so payload text can never become a span name, event name, or
// attribute; only a record that carries no event name at all (the pinned
// fixture, and any client predating event names) is named by its constant
// body.
func canonicalEventName(eventName, body string, attrs map[string]any) string {
	name := flattenEventName(eventName)
	if name == "" {
		return body
	}
	if attr, ok := kindAttrs[eventName]; ok {
		if kind := StringValue(attrs[attr]); kind != "" {
			name += "_" + kind
		}
	}
	return name
}

// ParseRecord claims a record for the Cursor edge when its instrumentation
// scope matches and it carries a conversation id. It declines codex-named
// records so the two logs edges stay disjoint whatever a payload contains.
func ParseRecord(record plog.LogRecord, scopeName string, resource pcommon.Resource) (Event, bool) {
	if !strings.HasPrefix(scopeName, cursorScopePrefix) {
		return Event{}, false
	}
	attrs := record.Attributes().AsRaw()
	eventName := wireEventName(record, attrs)
	if strings.HasPrefix(eventName, "codex.") {
		return Event{}, false
	}
	// conversation_content records carry prompt, response, or tool I/O text as
	// their body, so the edge declines them whole: they contribute no span,
	// event name, or attribute to canonical output.
	if isConversationContent(eventName, attrs) {
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
		Name:           canonicalEventName(eventName, body, attrs),
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
