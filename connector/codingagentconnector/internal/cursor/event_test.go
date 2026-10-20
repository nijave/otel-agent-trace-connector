// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

func testResource() pcommon.Resource {
	resource := pcommon.NewResource()
	resource.Attributes().PutStr("service.name", "cursor")
	resource.Attributes().PutStr("service.version", "1.16.5")
	resource.Attributes().PutInt("cursor.team.id", 4242)
	resource.Attributes().PutStr("cursor.surface", "cli")
	resource.Attributes().PutStr("cursor.entrypoint", "cli")
	return resource
}

func apiRequestRecord(ts time.Time, attrs map[string]any) plog.LogRecord {
	record := plog.NewLogRecord()
	record.Body().SetStr(NameAPIRequest)
	record.SetTimestamp(pcommon.NewTimestampFromTime(ts))
	// FromRaw replaces the whole map, so merge overrides into one map first.
	merged := map[string]any{
		"cursor.event.id":                          "customer-telemetry:v1:ev-1",
		"cursor.source_event.id":                   "src-1",
		"cursor.conversation.id":                   "11111111-2222-3333-4444-555555555555",
		"cursor.usage_event.id":                    "ue-1",
		"cursor.model.name":                        "claude-4.5-sonnet",
		"cursor.api.request.input_tokens":          100,
		"cursor.api.request.output_tokens":         200,
		"cursor.api.request.cache_read_tokens":     50,
		"cursor.api.request.cache_creation_tokens": 10,
	}
	for k, v := range attrs {
		merged[k] = v
	}
	requireNoError(record.Attributes().FromRaw(merged))
	return record
}

// requireNoError panics on a failed pdata bulk setter, mirroring the codex
// package's test helper for the same errcheck rule.
func requireNoError(err error) {
	if err != nil {
		panic(err)
	}
}

func TestParseRecordClaimsCursorScope(t *testing.T) {
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	event, ok := ParseRecord(apiRequestRecord(ts, nil), "cursor.telemetry", testResource())
	require.True(t, ok)
	require.Equal(t, NameAPIRequest, event.Name)
	require.Equal(t, "customer-telemetry:v1:ev-1", event.EventID)
	require.Equal(t, "11111111-2222-3333-4444-555555555555", event.ConversationID)
	require.Equal(t, "ue-1", event.UsageEventID)
	require.Equal(t, ts, event.Timestamp)
	require.Equal(t, "cursor", event.Resource["service.name"])
	require.Equal(t, int64(100), event.Attrs["cursor.api.request.input_tokens"])
}

func TestParseRecordToleratesScopeVersions(t *testing.T) {
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	_, ok := ParseRecord(apiRequestRecord(ts, nil), "cursor.telemetry/0.2.0", testResource())
	require.True(t, ok)
}

func TestParseRecordRejectsForeignScope(t *testing.T) {
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	_, ok := ParseRecord(apiRequestRecord(ts, nil), "codex_cli_rs", testResource())
	require.False(t, ok)
	_, ok = ParseRecord(apiRequestRecord(ts, nil), "other.telemetry", testResource())
	require.False(t, ok)
}

func TestParseRecordRejectsMissingConversationID(t *testing.T) {
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	record := apiRequestRecord(ts, nil)
	record.Attributes().Remove("cursor.conversation.id")
	_, ok := ParseRecord(record, "cursor.telemetry", testResource())
	require.False(t, ok)
}

func TestParseRecordSkipsCodexNamedRecords(t *testing.T) {
	// The claiming guard from the spec: never claim a record whose event.name
	// belongs to the Codex edge, whatever scope carries it.
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	record := apiRequestRecord(ts, nil)
	record.Attributes().PutStr("event.name", "codex.user_prompt")
	_, ok := ParseRecord(record, "cursor.telemetry", testResource())
	require.False(t, ok)
}

func TestParseRecordFallsBackToObservedTimestamp(t *testing.T) {
	observed := time.Date(2026, 8, 21, 10, 0, 5, 0, time.UTC)
	record := apiRequestRecord(time.Time{}, nil)
	record.SetTimestamp(0)
	record.SetObservedTimestamp(pcommon.NewTimestampFromTime(observed))
	event, ok := ParseRecord(record, "cursor.telemetry", testResource())
	require.True(t, ok)
	require.Equal(t, observed, event.Timestamp)
}

func TestParseRecordMarksInferredTimestamp(t *testing.T) {
	record := apiRequestRecord(time.Time{}, nil)
	record.SetTimestamp(0)
	record.SetObservedTimestamp(0)
	event, ok := ParseRecord(record, "cursor.telemetry", testResource())
	require.True(t, ok)
	require.False(t, event.Timestamp.IsZero())
	require.Equal(t, true, event.Attrs["coding_agent.timestamp.inferred"])
}

func TestParseRecordDerivesEventIDWhenAbsent(t *testing.T) {
	// The wire guarantees cursor.event.id, but a malformed record must not
	// dedupe against every other id-less record (the empty-string key would).
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	record := apiRequestRecord(ts, nil)
	record.Attributes().Remove("cursor.event.id")
	event, ok := ParseRecord(record, "cursor.telemetry", testResource())
	require.True(t, ok)
	require.NotEmpty(t, event.EventID)

	other := apiRequestRecord(ts.Add(time.Second), nil)
	other.Attributes().Remove("cursor.event.id")
	event2, ok := ParseRecord(other, "cursor.telemetry", testResource())
	require.True(t, ok)
	require.NotEqual(t, event.EventID, event2.EventID)
}

func TestParseRecordCoercesTokenStringsAndFloats(t *testing.T) {
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	record := apiRequestRecord(ts, map[string]any{
		"cursor.api.request.input_tokens": "300",
	})
	event, ok := ParseRecord(record, "cursor.telemetry", testResource())
	require.True(t, ok)
	value, ok := Int64Value(event.Attrs["cursor.api.request.input_tokens"])
	require.True(t, ok)
	require.Equal(t, int64(300), value)
}

func TestInt64ValueAcceptsTokenCountsUpToMaxInt64(t *testing.T) {
	// Same deliberate coercion family as the codex edge, so the same bound:
	// uint64 token counts in (1<<62, MaxInt64] are valid and must not be
	// silently dropped.
	value, ok := Int64Value(uint64(1<<62) + 1)
	require.True(t, ok)
	require.Equal(t, int64(1<<62)+1, value)

	value, ok = Int64Value(uint64(math.MaxInt64))
	require.True(t, ok)
	require.Equal(t, int64(math.MaxInt64), value)

	_, ok = Int64Value(uint64(1) << 63)
	require.False(t, ok)
}

func TestEventClassification(t *testing.T) {
	require.True(t, IsCorrectionEvent("api_correction_not_billed_errored"))
	require.True(t, IsCorrectionEvent("api_correction"))
	require.False(t, IsCorrectionEvent("api_request"))
}

func TestParseRecordNamesFromEventName(t *testing.T) {
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	record := apiRequestRecord(ts, nil)
	record.SetEventName("cursor.api.request")
	event, ok := ParseRecord(record, "cursor.telemetry", testResource())
	require.True(t, ok)
	require.Equal(t, NameAPIRequest, event.Name)

	// The event.name attribute carries the same name on a record that leaves
	// the OTLP field empty.
	attribute := apiRequestRecord(ts, map[string]any{"event.name": "cursor.skill.activated"})
	attribute.Body().SetStr("skill_activated")
	event, ok = ParseRecord(attribute, "cursor.telemetry", testResource())
	require.True(t, ok)
	require.Equal(t, "skill_activated", event.Name)
}

func TestParseRecordAppendsKindTheEventNameOmits(t *testing.T) {
	// The wire carries the correction kind in an attribute and in the body, not
	// in the event name, so the canonical name reconstructs the body's form.
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	record := apiRequestRecord(ts, map[string]any{"cursor.api.correction.kind": "not_billed_errored"})
	record.SetEventName("cursor.api.correction")
	request, ok := ParseRecord(record, "cursor.telemetry", testResource())
	require.True(t, ok)
	require.Equal(t, "api_correction_not_billed_errored", request.Name)

	setup := apiRequestRecord(ts, map[string]any{"cursor.cloud_agent.setup.kind": "completed"})
	setup.SetEventName("cursor.cloud_agent.setup")
	request, ok = ParseRecord(setup, "cursor.telemetry", testResource())
	require.True(t, ok)
	require.Equal(t, "cloud_agent_setup_completed", request.Name)
}

func TestParseRecordNeverNamesFromTheBodyWhenAnEventNameExists(t *testing.T) {
	// Payload text in the body must not become a name even when it looks like
	// an event name of the conversation_content family.
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	for _, body := range []string{
		"conversation_user_message: rewrite the deploy script",
		`{"path":"/tmp/secret.txt"}`,
	} {
		record := apiRequestRecord(ts, nil)
		record.SetEventName("cursor.cloud_agent.commit")
		record.Body().SetStr(body)
		event, ok := ParseRecord(record, "cursor.telemetry", testResource())
		require.True(t, ok)
		require.Equal(t, "cloud_agent_commit", event.Name)
	}
}

func TestParseRecordDeclinesConversationContent(t *testing.T) {
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	const prompt = "secret-free prompt text that must not reach canonical output"

	// The event name routes the family, whether it arrives in the OTLP field or
	// the event.name attribute.
	byField := apiRequestRecord(ts, nil)
	byField.SetEventName("cursor.conversation.user_message")
	byField.Body().SetStr(prompt)
	_, ok := ParseRecord(byField, "cursor.telemetry", testResource())
	require.False(t, ok)

	byAttribute := apiRequestRecord(ts, map[string]any{"event.name": "cursor.conversation.tool_io"})
	byAttribute.Body().SetStr(prompt)
	_, ok = ParseRecord(byAttribute, "cursor.telemetry", testResource())
	require.False(t, ok)

	// The always-present content_truncated attribute declines a record that
	// arrives with no event name at all, whatever its body holds.
	byAttributeOnly := apiRequestRecord(ts, map[string]any{"cursor.conversation.content_truncated": false})
	byAttributeOnly.Body().SetStr(prompt)
	_, ok = ParseRecord(byAttributeOnly, "cursor.telemetry", testResource())
	require.False(t, ok)
}

func TestParseRecordNamesUnknownBodyFromItsBodyWithoutAnEventName(t *testing.T) {
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	record := apiRequestRecord(ts, nil)
	record.Body().SetStr("future_event_kind")
	event, ok := ParseRecord(record, "cursor.telemetry", testResource())
	require.True(t, ok)
	require.Equal(t, "future_event_kind", event.Name)
}

func TestParseRecordHandlesNonStringBody(t *testing.T) {
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	record := apiRequestRecord(ts, nil)
	record.Body().SetEmptyMap().PutStr("kind", "api_request")
	event, ok := ParseRecord(record, "cursor.telemetry", testResource())
	require.True(t, ok)
	require.Equal(t, "", event.Name)
}
