package codingagentconnector

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

func TestParseEvent(t *testing.T) {
	record := plog.NewLogRecord()
	ts := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	record.SetTimestamp(pcommon.NewTimestampFromTime(ts))
	record.Attributes().PutStr("event.name", "codex.user_prompt")
	record.Attributes().PutStr("conversation.id", "conversation-1")
	resource := pcommon.NewResource()
	resource.Attributes().PutStr("service.name", "codex_cli_rs")

	event, ok := parseEvent(record, resource)
	require.True(t, ok)
	require.Equal(t, "codex.user_prompt", event.name)
	require.Equal(t, "conversation-1", event.conversationID)
	require.Equal(t, ts, event.timestamp)
	require.Equal(t, "codex_cli_rs", event.resource["service.name"])
}

func TestParseEventSupportsMapBody(t *testing.T) {
	record := plog.NewLogRecord()
	require.NoError(t, record.Body().SetEmptyMap().FromRaw(map[string]any{
		"event.name": "codex.sse_event", "conversation.id": "conversation-1", "event.kind": "response.completed",
	}))
	event, ok := parseEvent(record, pcommon.NewResource())
	require.True(t, ok)
	require.Equal(t, "response.completed", event.attrs["event.kind"])
	require.Equal(t, true, event.attrs["coding_agent.timestamp.inferred"])
}

func TestParseEventIgnoresUnsupportedRecords(t *testing.T) {
	tests := []map[string]string{
		{"event.name": "other.event", "conversation.id": "conversation-1"},
		{"event.name": "codex.user_prompt"},
	}
	for _, attrs := range tests {
		record := plog.NewLogRecord()
		for key, value := range attrs {
			record.Attributes().PutStr(key, value)
		}
		_, ok := parseEvent(record, pcommon.NewResource())
		require.False(t, ok)
	}
}

func TestValueConversions(t *testing.T) {
	value, ok := int64Value("42")
	require.True(t, ok)
	require.Equal(t, int64(42), value)
	boolean, ok := boolValue("true")
	require.True(t, ok)
	require.True(t, boolean)
}
