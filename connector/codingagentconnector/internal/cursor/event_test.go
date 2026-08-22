// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
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
	record.Body().SetStr(BodyAPIRequest)
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
	require.Equal(t, BodyAPIRequest, event.Body)
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

func TestBodyClassification(t *testing.T) {
	require.True(t, IsCorrectionBody("api_correction_not_billed_errored"))
	require.False(t, IsCorrectionBody("api_request"))
	require.True(t, IsCloudAgentBody("cloud_agent_setup_completed"))
	require.False(t, IsCloudAgentBody("hook_execution_complete"))
}

func TestParseRecordKeepsUnknownBody(t *testing.T) {
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	record := apiRequestRecord(ts, nil)
	record.Body().SetStr("future_event_kind")
	event, ok := ParseRecord(record, "cursor.telemetry", testResource())
	require.True(t, ok)
	require.Equal(t, "future_event_kind", event.Body)
}

func TestParseRecordHandlesNonStringBody(t *testing.T) {
	ts := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	record := apiRequestRecord(ts, nil)
	record.Body().SetEmptyMap().PutStr("kind", "api_request")
	event, ok := ParseRecord(record, "cursor.telemetry", testResource())
	require.True(t, ok)
	require.Equal(t, "", event.Body)
}
