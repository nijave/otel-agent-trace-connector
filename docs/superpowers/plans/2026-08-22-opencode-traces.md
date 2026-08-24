# OpenCode native-trace normalization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Normalize OpenCode's native OTel traces (Vercel AI SDK spans) into the canonical `invoke_agent`/`chat`/`execute_tool` vocabulary on the traces edge, dropping Effect-internal noise, with an opt-in live e2e stack and fixtures captured from it.

**Architecture:** New stateless `internal/opencode` traces edge mirroring `internal/claude`: claim resource groups whose instrumentation scope is exactly `opencode`, rebuild claimed groups keeping only the three AI SDK span types, rename them in place, and copy only allowlisted attributes so content cannot leak structurally. Wire into the existing `tracesRouter`.

**Tech Stack:** Go, OpenTelemetry Collector pdata/ptrace, testify; Docker Compose + Node image for the e2e runner.

**Spec:** `docs/superpowers/specs/2026-08-22-opencode-traces-design.md`

## Global Constraints

- Two independent Go modules, no `go.work`: connector module is `connector/codingagentconnector/`, e2e validator is repo root. Run tests in both.
- Lint parity with CI: golangci-lint v2.11.4, gofmt, vet. Full local gate: `./scripts/check.sh`.
- Canonical output never carries prompt/completion/tool content (`ai.response.text`, `ai.toolCall.args`, `ai.toolCall.result`) — enforced by attribute allowlist, not denylist.
- Allowlists over denylists everywhere a name list exists (claimed scopes, claimed span names).
- No new Go dependencies; only what both modules already import.
- Comments sparse and why-focused, matching neighboring normalizer files.
- `metadata.yaml` and generated files are untouched by this plan (no mdatagen regeneration needed).
- Raw pipelines stay untouched: the connector adds nothing to what raw exporters receive.
- pdata ID comparisons use the defined types (`pcommon.TraceID{…}`, `pcommon.SpanID{…}`), never `[16]byte`/`[8]byte` literals — testify's DeepEqual distinguishes them.

---

### Task 1: opencode package — claiming, rebuild skeleton, invoke_agent mapping

**Files:**
- Create: `connector/codingagentconnector/internal/opencode/normalizer.go`
- Test: `connector/codingagentconnector/internal/opencode/normalizer_test.go`

**Interfaces:**
- Consumes: `consumer.Traces`, `ptrace.Traces`, `pcommon.Map` (existing collector APIs used by sibling packages).
- Produces: `func New(next consumer.Traces) connector.Traces`; `func ContainsOpenCodeSpans(resourceSpans ptrace.ResourceSpans) bool`. Task 3 wires `New` into the router; Task 5's validator asserts the emitted shape.

- [ ] **Step 1: Write the failing tests**

Create `normalizer_test.go`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package opencode

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

type traceSink struct {
	mu     sync.Mutex
	traces []ptrace.Traces
}

func (*traceSink) Capabilities() consumer.Capabilities { return consumer.Capabilities{} }
func (s *traceSink) ConsumeTraces(_ context.Context, traces ptrace.Traces) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := ptrace.NewTraces()
	traces.CopyTo(copied)
	s.traces = append(s.traces, copied)
	return nil
}
func (s *traceSink) all() []ptrace.Traces {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ptrace.Traces(nil), s.traces...)
}

// newGroup appends a resource group with one scope and one named span and
// returns the span for further decoration.
func newGroup(traces ptrace.Traces, scopeName, spanName string) ptrace.Span {
	rs := traces.ResourceSpans().AppendEmpty()
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName(scopeName)
	span := ss.Spans().AppendEmpty()
	span.SetName(spanName)
	return span
}

func TestContainsOpenCodeSpansExactScopeOnly(t *testing.T) {
	input := ptrace.NewTraces()
	require.False(t, ContainsOpenCodeSpans(input.ResourceSpans().AppendEmpty()))

	exact := input.ResourceSpans().AppendEmpty()
	exact.ScopeSpans().AppendEmpty().Scope().SetName("opencode")
	require.True(t, ContainsOpenCodeSpans(exact))

	plugin := input.ResourceSpans().AppendEmpty()
	plugin.ScopeSpans().AppendEmpty().Scope().SetName("opencode.plugins")
	require.False(t, ContainsOpenCodeSpans(plugin), "prefix matches belong to plugins/Kilo")

	kilo := input.ResourceSpans().AppendEmpty()
	kilo.ScopeSpans().AppendEmpty().Scope().SetName("com.opencode")
	require.False(t, ContainsOpenCodeSpans(kilo))
}

func TestNormalizerClaimsGroupDropsNoiseAndKeepsIdentity(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "opencode")
	rs.Resource().Attributes().PutStr("service.version", "1.18.21")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("opencode")

	root := ss.Spans().AppendEmpty()
	root.SetName("ai.streamText")
	root.SetTraceID([16]byte{1})
	root.SetSpanID([8]byte{2})
	root.SetKind(ptrace.SpanKindInternal)
	root.Status().SetCode(ptrace.StatusCodeError)
	root.Status().SetMessage("boom")
	root.Attributes().PutStr("session.id", "ses_abc")
	root.Attributes().PutInt("ai.usage.inputTokens", 1000)
	root.Attributes().PutInt("ai.usage.outputTokens", 50)
	root.Attributes().PutInt("ai.usage.cachedInputTokens", 400)
	root.Attributes().PutStr("ai.response.text", "SECRET COMPLETION")

	effect := ss.Spans().AppendEmpty()
	effect.SetName("sql.execute")

	otherScope := rs.ScopeSpans().AppendEmpty()
	otherScope.Scope().SetName("some-lib")
	otherScope.Spans().AppendEmpty().SetName("http.client")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	require.Len(t, sink.all(), 1)

	outRS := sink.all()[0].ResourceSpans().At(0)
	var out []ptrace.Span
	for i := 0; i < outRS.ScopeSpans().Len(); i++ {
		spans := outRS.ScopeSpans().At(i).Spans()
		for j := 0; j < spans.Len(); j++ {
			out = append(out, spans.At(j))
		}
	}
	require.Len(t, out, 1, "Effect and non-opencode-scope spans must be dropped")

	span := out[0]
	require.Equal(t, "invoke_agent opencode", span.Name())
	require.Equal(t, pcommon.TraceID{1}, span.TraceID())
	require.Equal(t, pcommon.SpanID{2}, span.SpanID())
	require.Equal(t, ptrace.SpanKindInternal, span.Kind())
	require.Equal(t, ptrace.StatusCodeError, span.Status().Code())
	require.Equal(t, "boom", span.Status().Message())
	attrString := func(s ptrace.Span, key string) string {
		v, ok := s.Attributes().Get(key)
		require.True(t, ok, "%s must be present", key)
		return v.Str()
	}
	require.Equal(t, "ses_abc", attrString(span, "gen_ai.conversation.id"))
	require.Equal(t, "invoke_agent", attrString(span, "gen_ai.operation.name"))
	require.Equal(t, "opencode", attrString(span, "gen_ai.agent.name"))
	require.Equal(t, "native", attrString(span, "coding_agent.source"))
	require.Equal(t, "opencode", attrString(span, "coding_agent.client.name"))
	require.Equal(t, "1.18.21", attrString(span, "coding_agent.client.version"))
	require.Equal(t, "ai.streamText", attrString(span, "coding_agent.source.event"))

	usageInt := func(s ptrace.Span, key string) int64 {
		v, ok := s.Attributes().Get(key)
		require.True(t, ok, "%s must be present", key)
		return v.Int()
	}
	require.Equal(t, int64(1000), usageInt(span, "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(50), usageInt(span, "gen_ai.usage.output_tokens"))
	require.Equal(t, int64(400), usageInt(span, "gen_ai.usage.cache_read.input_tokens"))

	for _, forbidden := range []string{"ai.response.text", "ai.usage.inputTokens", "session.id"} {
		_, ok := span.Attributes().Get(forbidden)
		require.False(t, ok, "%s must not reach canonical output", forbidden)
	}

	require.Equal(t, "sql.execute", input.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(1).Name(),
		"input must not be mutated")
}

func TestNormalizerFallsBackToResourceSessionID(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("session.id", "ses_resource")
	newSpan := newGroup(input, "opencode", "ai.streamText")
	_ = newSpan

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	v, ok := out.Attributes().Get("gen_ai.conversation.id")
	require.True(t, ok)
	require.Equal(t, "ses_resource", v.Str())
}

func TestNormalizerMissingUsageEmitsWithoutTokens(t *testing.T) {
	input := ptrace.NewTraces()
	newGroup(input, "opencode", "ai.streamText")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	_, hasInput := out.Attributes().Get("gen_ai.usage.input_tokens")
	require.False(t, hasInput, "missing wire usage must stay absent, not zero-filled")
}

func TestNormalizerEmitsNothingWithoutClaimedGroups(t *testing.T) {
	input := ptrace.NewTraces()
	newGroup(input, "com.opencode", "opencode.llm")
	newGroup(input, "my-app", "run")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	require.Empty(t, sink.all())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd connector/codingagentconnector && go test ./internal/opencode/ -v`
Expected: FAIL — package does not exist (build error).

- [ ] **Step 3: Write minimal implementation**

Create `normalizer.go`. Task 1 ships only the `wireStreamText` case; Task 2 extends the two switches.

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package opencode

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

const (
	scopeName  = "opencode"
	clientName = "opencode"
	agentName  = "opencode"

	wireStreamText = "ai.streamText"
)

// opencodeTraceNormalizer rewrites OpenCode's native Vercel AI SDK spans into
// the canonical vocabulary and drops everything else OpenCode exports. It is
// stateless: children can land in exports without their ancestors, so each
// batch is rewritten as-is and backends reassemble by the preserved IDs.
type opencodeTraceNormalizer struct {
	next consumer.Traces
	component.StartFunc
	component.ShutdownFunc
}

// New creates the stateless OpenCode native traces-to-traces edge.
func New(next consumer.Traces) connector.Traces {
	return &opencodeTraceNormalizer{next: next}
}

func (*opencodeTraceNormalizer) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (n *opencodeTraceNormalizer) ConsumeTraces(ctx context.Context, input ptrace.Traces) error {
	output := ptrace.NewTraces()
	for i := 0; i < input.ResourceSpans().Len(); i++ {
		inputResourceSpans := input.ResourceSpans().At(i)
		if !ContainsOpenCodeSpans(inputResourceSpans) {
			continue
		}
		rs := output.ResourceSpans().AppendEmpty()
		inputResourceSpans.Resource().CopyTo(rs.Resource())
		version := resourceString(rs.Resource(), "service.version")
		resourceSessionID := resourceString(rs.Resource(), "session.id")
		for j := 0; j < inputResourceSpans.ScopeSpans().Len(); j++ {
			inputScopeSpans := inputResourceSpans.ScopeSpans().At(j)
			ss := rs.ScopeSpans().AppendEmpty()
			inputScopeSpans.Scope().CopyTo(ss.Scope())
			spans := ss.Spans()
			for k := 0; k < inputScopeSpans.Spans().Len(); k++ {
				wire := inputScopeSpans.Spans().At(k)
				if !isClaimedSpan(wire.Name()) {
					continue
				}
				span := spans.AppendEmpty()
				copySpanMetadata(wire, span)
				normalizeSpan(wire, span, version, resourceSessionID)
			}
		}
		rs.ScopeSpans().RemoveIf(func(ss ptrace.ScopeSpans) bool { return ss.Spans().Len() == 0 })
	}
	output.ResourceSpans().RemoveIf(func(rs ptrace.ResourceSpans) bool { return rs.ScopeSpans().Len() == 0 })
	if output.SpanCount() == 0 {
		return nil
	}
	return n.next.ConsumeTraces(ctx, output)
}

// ContainsOpenCodeSpans reports whether any scope in the group is OpenCode's
// native tracer. Exact match: prefixed scopes such as opencode.* plugins or
// Kilo's com.opencode are separate surfaces this edge must not claim.
func ContainsOpenCodeSpans(resourceSpans ptrace.ResourceSpans) bool {
	for i := 0; i < resourceSpans.ScopeSpans().Len(); i++ {
		if resourceSpans.ScopeSpans().At(i).Scope().Name() == scopeName {
			return true
		}
	}
	return false
}

// isClaimedSpan lists the exact wire span names rewritten into canonical
// vocabulary; every other span OpenCode exports is internal Effect
// instrumentation and never enters canonical output.
func isClaimedSpan(name string) bool {
	switch name {
	case wireStreamText:
		return true
	}
	return false
}

func normalizeSpan(wire, span ptrace.Span, version, resourceSessionID string) {
	attrs := span.Attributes()
	putCommon(attrs, version)
	attrs.PutStr("coding_agent.source.event", wire.Name())
	switch wire.Name() {
	case wireStreamText:
		sessionID := firstString(wire.Attributes(), "session.id")
		if sessionID == "" {
			sessionID = resourceSessionID
		}
		if sessionID != "" {
			attrs.PutStr("gen_ai.conversation.id", sessionID)
		}
		attrs.PutStr("gen_ai.operation.name", "invoke_agent")
		attrs.PutStr("gen_ai.agent.name", agentName)
		copyUsage(wire.Attributes(), attrs)
		span.SetName("invoke_agent " + agentName)
	}
}

func putCommon(attrs pcommon.Map, version string) {
	attrs.PutStr("coding_agent.source", "native")
	attrs.PutStr("coding_agent.client.name", clientName)
	if version != "" {
		attrs.PutStr("coding_agent.client.version", version)
	}
}

var usageKeys = [][2]string{
	{"ai.usage.inputTokens", "gen_ai.usage.input_tokens"},
	{"ai.usage.outputTokens", "gen_ai.usage.output_tokens"},
	{"ai.usage.cachedInputTokens", "gen_ai.usage.cache_read.input_tokens"},
}

func copyUsage(from, to pcommon.Map) {
	for _, pair := range usageKeys {
		if value, ok := from.Get(pair[0]); ok && value.Type() == pcommon.ValueTypeInt {
			to.PutInt(pair[1], value.Int())
		}
	}
}

func firstString(attrs pcommon.Map, key string) string {
	value, ok := attrs.Get(key)
	if !ok || value.Str() == "" {
		return ""
	}
	return value.Str()
}

func resourceString(resource pcommon.Resource, key string) string {
	value, ok := resource.Attributes().Get(key)
	if !ok {
		return ""
	}
	return value.Str()
}

func copySpanMetadata(wire, span ptrace.Span) {
	span.SetTraceID(wire.TraceID())
	span.SetSpanID(wire.SpanID())
	span.SetParentSpanID(wire.ParentSpanID())
	span.SetKind(wire.Kind())
	span.SetStartTimestamp(wire.StartTimestamp())
	span.SetEndTimestamp(wire.EndTimestamp())
	span.SetFlags(wire.Flags())
	span.SetDroppedAttributesCount(wire.DroppedAttributesCount())
	status := wire.Status()
	span.Status().SetCode(status.Code())
	span.Status().SetMessage(status.Message())
}

var _ connector.Traces = (*opencodeTraceNormalizer)(nil)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd connector/codingagentconnector && go test ./internal/opencode/ -v`
Expected: PASS (all six tests).

- [ ] **Step 5: Lint**

Run: `cd connector/codingagentconnector && gofmt -l . && go vet ./internal/opencode/`
Expected: no output, exit 0.

- [ ] **Step 6: Commit**

```bash
git add connector/codingagentconnector/internal/opencode/
git commit -m "feat(opencode): claim native AI SDK step roots as invoke_agent"
```

---

### Task 2: chat and execute_tool mappings

**Files:**
- Modify: `connector/codingagentconnector/internal/opencode/normalizer.go`
- Test: `connector/codingagentconnector/internal/opencode/normalizer_test.go`

**Interfaces:**
- Consumes: Task 1's `normalizeSpan` switch and `isClaimedSpan`.
- Produces: complete mapping surface — `chat {model}` from `ai.streamText.doStream`, `execute_tool {tool}` from `ai.toolCall`. Task 5's validator relies on these exact names/attributes.

- [ ] **Step 1: Add the failing tests**

Append to `normalizer_test.go`:

```go
func TestNormalizerRenamesDoStreamAndToolCall(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("opencode")

	doStream := ss.Spans().AppendEmpty()
	doStream.SetName("ai.streamText.doStream")
	doStream.SetSpanID([8]byte{3})
	doStream.SetParentSpanID([8]byte{2})
	doStream.Attributes().PutStr("gen_ai.request.model", "ox-alpha-free")
	doStream.Attributes().PutStr("gen_ai.response.id", "resp_1")
	doStream.Attributes().PutStr("gen_ai.response.finish_reasons", "stop")

	tool := ss.Spans().AppendEmpty()
	tool.SetName("ai.toolCall")
	tool.SetSpanID([8]byte{4})
	tool.SetParentSpanID([8]byte{2})
	tool.Attributes().PutStr("ai.toolCall.name", "bash")
	tool.Attributes().PutStr("ai.toolCall.id", "call_1")
	tool.Attributes().PutStr("ai.toolCall.args", "SECRET ARGS")
	tool.Attributes().PutStr("ai.toolCall.result", "SECRET RESULT")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	spans := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans()

	require.Equal(t, "chat ox-alpha-free", spans.At(0).Name())
	v, ok := spans.At(0).Attributes().Get("gen_ai.operation.name")
	require.True(t, ok)
	require.Equal(t, "chat", v.Str())
	_, leaked := spans.At(0).Attributes().Get("gen_ai.response.id")
	require.False(t, leaked, "response metadata beyond model stays off canonical chat")

	require.Equal(t, "execute_tool bash", spans.At(1).Name())
	toolAttrs := spans.At(1).Attributes()
	name, ok := toolAttrs.Get("gen_ai.tool.name")
	require.True(t, ok)
	require.Equal(t, "bash", name.Str())
	op, ok := toolAttrs.Get("gen_ai.operation.name")
	require.True(t, ok)
	require.Equal(t, "execute_tool", op.Str())
	for _, secret := range []string{"ai.toolCall.args", "ai.toolCall.result", "ai.toolCall.id"} {
		_, leaked := toolAttrs.Get(secret)
		require.False(t, leaked, "%s must not reach canonical output", secret)
	}
	require.Equal(t, pcommon.SpanID{3}, spans.At(0).SpanID(), "IDs pass through for backend reassembly")
	require.Equal(t, pcommon.SpanID{4}, spans.At(1).SpanID())
}

func TestNormalizerBareNamesWhenSubjectMissing(t *testing.T) {
	input := ptrace.NewTraces()
	newGroup(input, "opencode", "ai.streamText.doStream")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, "chat", out.Name())

	input2 := ptrace.NewTraces()
	newGroup(input2, "opencode", "ai.toolCall")
	sink2 := &traceSink{}
	require.NoError(t, New(sink2).ConsumeTraces(context.Background(), input2))
	out2 := sink2.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, "execute_tool", out2.Name())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd connector/codingagentconnector && go test ./internal/opencode/ -run 'TestNormalizer' -v`
Expected: FAIL — doStream/toolCall spans are dropped today (`spans.At(1)` index errors).

- [ ] **Step 3: Extend the two switches**

In `normalizer.go` add constants next to `wireStreamText`:

```go
	wireDoStream = "ai.streamText.doStream"
	wireToolCall = "ai.toolCall"
```

Extend `isClaimedSpan`:

```go
	switch name {
	case wireStreamText, wireDoStream, wireToolCall:
		return true
	}
	return false
```

Extend `normalizeSpan`'s switch after the `wireStreamText` case:

```go
	case wireDoStream:
		model := firstString(wire.Attributes(), "gen_ai.request.model")
		name := "chat"
		if model != "" {
			attrs.PutStr("gen_ai.request.model", model)
			name += " " + model
		}
		attrs.PutStr("gen_ai.operation.name", "chat")
		span.SetName(name)
	case wireToolCall:
		tool := firstString(wire.Attributes(), "ai.toolCall.name")
		name := "execute_tool"
		if tool != "" {
			attrs.PutStr("gen_ai.tool.name", tool)
			name += " " + tool
		}
		attrs.PutStr("gen_ai.operation.name", "execute_tool")
		span.SetName(name)
```

Note the deliberate asymmetry with the root: response id / finish reasons / tool call ids have no established canonical home, so only the naming subject survives.

- [ ] **Step 4: Run all package tests to verify they pass**

Run: `cd connector/codingagentconnector && go test -race ./internal/opencode/ -v`
Expected: PASS (all eight tests).

- [ ] **Step 5: Lint**

Run: `cd connector/codingagentconnector && gofmt -l . && go vet ./internal/opencode/`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add connector/codingagentconnector/internal/opencode/
git commit -m "feat(opencode): map per-request chat and tool-call children"
```

---

### Task 3: Router wiring and disjointness test

**Files:**
- Modify: `connector/codingagentconnector/traces.go:29` (edges slice) and imports
- Test: `connector/codingagentconnector/traces_test.go` (extend existing test)

**Interfaces:**
- Consumes: `opencode.New(next consumer.Traces) connector.Traces` (Task 1), `opencode.ContainsOpenCodeSpans` unused here — claiming happens inside the edge itself.
- Produces: the live traces edge now routes OpenCode groups exactly once.

- [ ] **Step 1: Extend the failing router test**

In `traces_test.go`, inside `TestTracesRouterSendsEachGroupToExactlyOneNormalizer` after `unknownGroup` is built, insert:

```go
	opencodeGroup := input.ResourceSpans().AppendEmpty()
	opencodeScope := opencodeGroup.ScopeSpans().AppendEmpty()
	opencodeScope.Scope().SetName("opencode")
	step := opencodeScope.Spans().AppendEmpty()
	step.SetName("ai.streamText")
	step.Attributes().PutStr("session.id", "ses_router")
```

And update the closing assertions of that test to:

```go
	require.Equal(t, 3, total, "unknown groups stay out of the canonical edge")
	require.Equal(t, 1, names["invoke_agent claude_code"], "claude normalizer claimed its group once")
	require.Equal(t, 1, names["chat glm-4.7"], "genai normalizer claimed its group once")
	require.Equal(t, 1, names["invoke_agent opencode"], "opencode normalizer claimed its group once")
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd connector/codingagentconnector && go test . -run TestTracesRouter -v`
Expected: FAIL — `invoke_agent opencode` count 0.

- [ ] **Step 3: Wire the edge**

In `traces.go` add the import and change the edges slice:

```go
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/opencode"
```

```go
	return &tracesRouter{edges: []connector.Traces{claude.New(next), genai.New(next), opencode.New(next)}}
```

- [ ] **Step 4: Run full module suite to verify it passes**

Run: `cd connector/codingagentconnector && go test -race ./...`
Expected: PASS, including generated lifecycle tests.

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/traces.go connector/codingagentconnector/traces_test.go
git commit -m "feat(opencode): route native OpenCode traces into the canonical edge"
```

---

### Task 4: Live e2e stack

**Files:**
- Create: `compose.e2e-opencode.yaml`
- Create: `e2e/opencode/Dockerfile`
- Create: `e2e/opencode/opencode.json`
- Create: `e2e/opencode/run.sh`
- Create: `scripts/e2e-opencode.sh`
- Modify: `scripts/check.sh` (compose loop + image build)

**Interfaces:**
- Consumes: `scripts/lib-e2e.sh` `e2e_run` helper; `compose.e2e-base.yaml` collector service.
- Produces: `.e2e-output/raw-traces.json` + `.e2e-output/canonical-traces.json` for run ID; `E2E_AGENT=opencode` contract consumed by Task 5's validator; fixture source for Task 6.

- [ ] **Step 1: Verify upstream config surface against the pinned version**

Before authoring config, confirm at https://github.com/sst/opencode (tag matching the pinned npm version below) that: the config key path `experimental.openTelemetry` still enables the OTel exporter, `{env:VAR}` interpolation works for provider api keys, and `opencode run -m provider/model` selects a custom provider. Adjust the JSON below if the schema moved; record nothing extra.

- [ ] **Step 2: Create the Compose stack**

`compose.e2e-opencode.yaml`:

```yaml
# OpenCode (z.ai) e2e stack. The shared collector comes from compose.e2e-base.yaml;
# only the OpenCode agent is defined here, so this stack requires just the z.ai key.
# Native telemetry: experimental.openTelemetry plus OTEL_EXPORTER_OTLP_ENDPOINT,
# exported while `opencode run` executes.
include:
  - compose.e2e-base.yaml

services:
  agent:
    build:
      context: e2e/opencode
      args:
        OPENCODE_VERSION: "${OPENCODE_VERSION:-1.18.21}"
    environment:
      # scripts/e2e-opencode.sh checks this credential before Compose runs, so it is
      # optional here; a required (:?) guard would only fire later and less clearly.
      OPENAI_API_KEY: "${OPENAI_API_KEY:-}"
      E2E_AGENT_TIMEOUT: "${E2E_AGENT_TIMEOUT:-10m}"
      E2E_OPENCODE_MODEL: "${E2E_OPENCODE_MODEL:-glm-4.7}"
      E2E_RUN_ID: "${E2E_RUN_ID:?set E2E_RUN_ID or run scripts/e2e-opencode.sh}"
      OTEL_EXPORTER_OTLP_ENDPOINT: http://collector:4318
      OTEL_EXPORTER_OTLP_PROTOCOL: http/protobuf
    depends_on:
      collector:
        condition: service_healthy
```

- [ ] **Step 3: Create the agent image**

`e2e/opencode/Dockerfile`:

```dockerfile
FROM node:24-bookworm-slim

ARG OPENCODE_VERSION=1.18.21
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
        ca-certificates git \
    && rm -rf /var/lib/apt/lists/*
RUN npm install --global "opencode-ai@${OPENCODE_VERSION}"

# Provider and native-OTel configuration. The z.ai key arrives at runtime via
# {env:OPENAI_API_KEY}; nothing secret is baked into the image.
RUN mkdir -p /root/.config/opencode
COPY opencode.json /root/.config/opencode/opencode.json

RUN mkdir -p /work
COPY run.sh /usr/local/bin/run-opencode-e2e
RUN chmod 0555 /usr/local/bin/run-opencode-e2e

WORKDIR /work
ENTRYPOINT ["/usr/local/bin/run-opencode-e2e"]
```

`e2e/opencode/opencode.json`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "experimental": {
    "openTelemetry": true
  },
  "provider": {
    "zai": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "z.ai (OpenAI-compatible)",
      "options": {
        "baseURL": "https://api.z.ai/api/coding/paas/v4",
        "apiKey": "{env:OPENAI_API_KEY}"
      },
      "models": {
        "glm-4.7": {},
        "glm-4.5-air": {}
      }
    }
  }
}
```

`e2e/opencode/run.sh`:

```sh
#!/bin/sh
set -eu

if [ -z "${OPENAI_API_KEY:-}" ]; then
  echo "OPENAI_API_KEY (your z.ai API key) is required" >&2
  exit 2
fi

git init -q .
exec timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" \
  opencode run -m "zai/${E2E_OPENCODE_MODEL:-glm-4.7}" \
    "Use the bash tool exactly once to run 'printf opencode-otel-e2e'. Then reply with only: done."
```

- [ ] **Step 4: Create the driver script**

`scripts/e2e-opencode.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

# The container receives one credential: the z.ai API key, passed to OpenCode as
# OPENAI_API_KEY against z.ai's OpenAI-compatible coding endpoint.
if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "OPENAI_API_KEY (your z.ai API key) is required; this test runs a real paid model." >&2
  exit 2
fi

export E2E_OPENCODE_MODEL="${E2E_OPENCODE_MODEL:-glm-4.7}"
# Selects the OpenCode validation path in the shared validator.
export E2E_AGENT=opencode

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib-e2e.sh
. "${script_dir}/lib-e2e.sh"

compose_files=(-f compose.e2e-opencode.yaml)
# The OpenCode stack only needs the shared collector; it talks to z.ai directly.
support_services=(collector)
e2e_run opencode
```

Then `chmod +x scripts/e2e-opencode.sh` and ensure `e2e/opencode/run.sh` is executable in git (`git update-index --chmod=+x e2e/opencode/run.sh scripts/e2e-opencode.sh`).

- [ ] **Step 5: Teach check.sh about the stack**

In `scripts/check.sh`, change the loop line:

```bash
for stack in openai strands opencode; do
```

And add the image build after `strands-e2e`:

```bash
docker build --tag opencode-e2e:check e2e/opencode
```

- [ ] **Step 6: Verify unpaid checks**

Run: `docker compose -f compose.e2e-opencode.yaml config --quiet && docker build --tag opencode-e2e:check e2e/opencode && bash -n scripts/e2e-opencode.sh && shellcheck scripts/e2e-opencode.sh e2e/opencode/run.sh`
Expected: all clean.

- [ ] **Step 7: Commit**

```bash
git add compose.e2e-opencode.yaml e2e/opencode/ scripts/e2e-opencode.sh scripts/check.sh
git commit -m "test(e2e): add opt-in OpenCode live stack"
```

---

### Task 5: Validator support for the OpenCode run

**Files:**
- Modify: `e2e/validator/validator.go`
- Modify: `e2e/validator/live_test.go`
- Test: `e2e/validator/validator_test.go`

**Interfaces:**
- Consumes: existing helpers `validateTraceFile`, `collectRunSpans`, `firstValidRoot`, `rejectGenAIContent`, and `stringAttr(span ptrace.Span, key string) string` (validator.go:261).
- Produces: `validateOpenCodeRawFile(path, runID string) error`; `rejectOpenCodeContent(spans []ptrace.Span) error`; `E2E_AGENT=opencode` accepted by `TestLiveE2ETraces`.

- [ ] **Step 1: Write failing unit tests**

Append to `validator_test.go`:

```go
func TestRejectOpenCodeContent(t *testing.T) {
	leaky := ptrace.NewTraces()
	span := leaky.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("invoke_agent opencode")
	span.Attributes().PutStr("ai.response.text", "leak")
	err := rejectOpenCodeContent(collectAllSpans(leaky))
	require.ErrorContains(t, err, "ai.response.text")

	clean := ptrace.NewTraces()
	ok := clean.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	ok.SetName("invoke_agent opencode")
	ok.Attributes().PutInt("gen_ai.usage.input_tokens", 10)
	require.NoError(t, rejectOpenCodeContent(collectAllSpans(clean)))
}

func TestValidateOpenCodeRawTraces(t *testing.T) {
	raw := ptrace.NewTraces()
	rs := raw.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("e2e.run.id", "run-1")
	ss := rs.ScopeSpans().AppendEmpty()
	llm := ss.Spans().AppendEmpty()
	llm.SetName("ai.streamText")
	noise := ss.Spans().AppendEmpty()
	noise.SetName("sql.execute")
	tool := ss.Spans().AppendEmpty()
	tool.SetName("ai.toolCall")
	tool.Attributes().PutStr("ai.toolCall.name", "bash")

	require.NoError(t, validateOpenCodeRawTraces(raw, "run-1"))
	require.Error(t, validateOpenCodeRawTraces(ptrace.NewTraces(), "run-1"), "empty run must fail")
}
```

If the package has no span-flattening helper yet, add it (same loop shape as `collectRunSpans` minus the run-id filter):

```go
func collectAllSpans(traces ptrace.Traces) []ptrace.Span {
	var spans []ptrace.Span
	for i := 0; i < traces.ResourceSpans().Len(); i++ {
		rs := traces.ResourceSpans().At(i)
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			ss := rs.ScopeSpans().At(j).Spans()
			for k := 0; k < ss.Len(); k++ {
				spans = append(spans, ss.At(k))
			}
		}
	}
	return spans
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./e2e/validator/ -run 'OpenCode' -v`
Expected: FAIL — `rejectOpenCodeContent` / `validateOpenCodeRawTraces` undefined.

- [ ] **Step 3: Implement the validators**

In `validator.go` add:

```go
func validateOpenCodeRawFile(path, runID string) error {
	return validateTraceFile(path, runID, validateOpenCodeRawTraces)
}

func validateOpenCodeRawTraces(traces ptrace.Traces, runID string) error {
	spans := collectRunSpans(traces, runID)
	if len(spans) == 0 {
		return errors.New("opencode run id was not found")
	}
	var llm, tool bool
	for _, span := range spans {
		switch span.Name() {
		case "ai.streamText":
			llm = true
		case "ai.toolCall":
			if stringAttr(span, "ai.toolCall.name") == "bash" {
				tool = true
			}
		}
	}
	if !llm || !tool {
		return errors.New("raw OpenCode LLM or bash tool span is missing")
	}
	return nil
}

// rejectOpenCodeContent fails if any AI-SDK content attribute reached a
// canonical span. The raw destination is allowed — and expected — to carry it.
func rejectOpenCodeContent(spans []ptrace.Span) error {
	for _, span := range spans {
		for _, key := range []string{"ai.response.text", "ai.toolCall.args", "ai.toolCall.result"} {
			if _, ok := span.Attributes().Get(key); ok {
				return fmt.Errorf("sensitive OpenCode attribute %q was captured on %q", key, span.Name())
			}
		}
	}
	return nil
}
```

(Uses the package's span-based `stringAttr` — see validator.go:261.)

In `validateCanonicalTraces`, add the case alongside `strands`:

```go
	case "opencode":
		if err := rejectGenAIContent(spans); err != nil {
			return err
		}
		if err := rejectOpenCodeContent(spans); err != nil {
			return err
		}
```

In the shared `firstValidRoot` body, add the OpenCode-specific assertions next to the Codex/Claude ones:

- After the conversation-id check:

```go
		if agent == "opencode" {
			if _, ok := root.Attributes().Get("gen_ai.usage.input_tokens"); !ok {
				return errors.New("opencode root usage is missing")
			}
			if stringAttr(root, "coding_agent.client.name") != "opencode" {
				return errors.New("opencode client name is missing")
			}
		}
```

- In the child switch, extend the `execute_tool` branch:

```go
			case "execute_tool":
				tool = true
				if agent == "claude_code" && stringAttr(child, "gen_ai.tool.name") != "Bash" {
					return errors.New("claude Bash tool span is missing")
				}
				if agent == "opencode" && stringAttr(child, "gen_ai.tool.name") != "bash" {
					return errors.New("opencode bash tool span is missing")
				}
```

(The `chat` branch's non-Codex requirement that `gen_ai.request.model` be set already covers OpenCode.)

- [ ] **Step 4: Accept the agent in the live gate**

In `live_test.go`: add `"opencode"` to the accepted-agent switch; require `RAW_TRACE_FILE` when `agent == "opencode"`; and in the poll loop add, mirroring the strands line:

```go
		if lastErr == nil && agent == "opencode" {
			lastErr = validateOpenCodeRawFile(rawPath, runID)
		}
```

- [ ] **Step 5: Run unit tests and lint**

Run: `go test ./e2e/validator/ && gofmt -l e2e/validator && go vet ./e2e/validator/`
Expected: PASS, clean.

- [ ] **Step 6: Commit**

```bash
git add e2e/validator/
git commit -m "test(e2e): validate OpenCode canonical and raw traces"
```

---

### Task 6: Fixture capture from a live run + replay test

**Prerequisite:** Tasks 1–5 merged into the working tree. This task runs the paid live stack once (explicit human go-ahead required; costs one small GLM session) and turns its raw output into a permanent regression fixture.

**Files:**
- Create: `connector/codingagentconnector/internal/opencode/testdata/opencode-native-traces.json`
- Modify: `connector/codingagentconnector/internal/opencode/normalizer_test.go`
- Modify: `e2e/README.md` (capture procedure section)

**Interfaces:**
- Consumes: `scripts/e2e-opencode.sh` outputs under `.e2e-output/`.
- Produces: committed fixture `opencode-native-traces.json`; replay test guarding the mapping against upstream wire drift.

- [ ] **Step 1: Confirm before spending**

Ask the operator: running this incurs a real model charge. Do not proceed without an explicit yes.

- [ ] **Step 2: Run the live stack**

```bash
export OPENAI_API_KEY=...   # z.ai key
./scripts/e2e-opencode.sh
```

Expected: agent runs, validator passes (`TestLiveE2ETraces` green for `E2E_AGENT=opencode`). If the agent produces no `ai.toolCall bash` span, adjust only the prompt wording in `run.sh` and retry once.

- [ ] **Step 3: Slice the fixture**

Keep the first resource group containing a streamText subtree (it includes its Effect-noise siblings, which the replay test needs):

```bash
jq -s '{resourceSpans: ([.[] | .resourceSpans[]?]
        | map(select(any(.scopeSpans[]?; any(.spans[]?; .name == "ai.streamText")))))[:1]}' \
  .e2e-output/raw-traces.json \
  > connector/codingagentconnector/internal/opencode/testdata/opencode-native-traces.json
```

Trim an oversized group to a bounded representative slice (fixture should stay well under ~200 spans; keep every `ai.*` span, sample the rest):

```bash
jq '.resourceSpans[0].scopeSpans |= map(.spans |= ((map(select(.name | startswith("ai.")))) + ([.[] | select((.name | startswith("ai.") | not))][:20])))' \
  connector/codingagentconnector/internal/opencode/testdata/opencode-native-traces.json \
  > /tmp/trimmed.json && mv /tmp/trimmed.json \
  connector/codingagentconnector/internal/opencode/testdata/opencode-native-traces.json
```

Sanity-check the slice kept the tree intact (streamText present, at least one doStream and one toolCall, at least one noise span):

```bash
jq '[.resourceSpans[].scopeSpans[].spans[].name] | length,
     any(. == "ai.streamText"), any(. == "ai.streamText.doStream"),
     any(. == "ai.toolCall"), (any(. == "sql.execute"))' \
  connector/codingagentconnector/internal/opencode/testdata/opencode-native-traces.json
```

Expected: five lines, all booleans `true`.

- [ ] **Step 4: Write the replay test**

Append to `normalizer_test.go` (add `"os"`, `"path/filepath"`, `"strings"` to imports):

```go
func countSpans(traces ptrace.Traces) int {
	total := 0
	for i := 0; i < traces.ResourceSpans().Len(); i++ {
		rs := traces.ResourceSpans().At(i)
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			total += rs.ScopeSpans().At(j).Spans().Len()
		}
	}
	return total
}

func anySpanNameWithPrefix(names map[string]bool, prefix string) bool {
	for name := range names {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func TestNormalizerFixtureReplay(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "opencode-native-traces.json"))
	require.NoError(t, err, "run scripts/e2e-opencode.sh and capture the fixture first")
	unmarshaler := &ptrace.JSONUnmarshaler{}
	input, err := unmarshaler.UnmarshalTraces(data)
	require.NoError(t, err)
	inputSpans := countSpans(input)
	require.Positive(t, inputSpans)

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	require.Len(t, sink.all(), 1)
	output := sink.all()[0]
	require.Less(t, countSpans(output), inputSpans, "Effect noise must be dropped")

	names := map[string]bool{}
	roots := 0
	for i := 0; i < output.ResourceSpans().Len(); i++ {
		rs := output.ResourceSpans().At(i)
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				names[span.Name()] = true
				for _, secret := range []string{"ai.response.text", "ai.toolCall.args", "ai.toolCall.result"} {
					_, leaked := span.Attributes().Get(secret)
					require.False(t, leaked, "%s must not survive normalization", secret)
				}
				if strings.HasPrefix(span.Name(), "invoke_agent") {
					roots++
				}
			}
		}
	}
	for name := range names {
		switch strings.SplitN(name, " ", 2)[0] {
		case "invoke_agent", "chat", "execute_tool":
		default:
			t.Fatalf("unexpected canonical span name %q", name)
		}
	}
	require.Positive(t, roots, "fixture must contain at least one step root")
	require.True(t, anySpanNameWithPrefix(names, "chat"), "fixture must contain a chat child")
	require.True(t, anySpanNameWithPrefix(names, "execute_tool"), "fixture must contain an execute_tool child")
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd connector/codingagentconnector && go test -race ./internal/opencode/ -v`
Expected: PASS including the replay test.

- [ ] **Step 6: Document the procedure**

Add an "## Live OpenCode E2E" section to `e2e/README.md` following the Codex section's shape: prerequisites (z.ai key), command, env overrides (`OPENCODE_VERSION`, `E2E_OPENCODE_MODEL`, `E2E_AGENT_TIMEOUT`), and a "Fixture refresh" subsection describing Step 3's jq slicing verbatim.

- [ ] **Step 7: Commit**

```bash
git add connector/codingagentconnector/internal/opencode/ e2e/README.md
git commit -m "test(opencode): replay live-captured wire fixture through the normalizer"
```

---

### Task 7: Docs and full verification

**Files:**
- Modify: `README.md`
- Modify: `docs/design.md`
- Modify: `docs/harnesses.md`
- Modify: `connector/codingagentconnector/README.md`
- (Task 6 already modified `e2e/README.md`.)

**Interfaces:**
- Consumes: everything above; documents the shipped behavior.
- Produces: documentation parity with the other harnesses.

- [ ] **Step 1: README harness list**

In `README.md`, add a bullet after the Cursor bullet:

```markdown
- **OpenCode:** renames its native Vercel AI SDK spans (`ai.streamText`,
  `ai.streamText.doStream`, `ai.toolCall`) into one `invoke_agent opencode`
  canonical trace per model step, dropping internal instrumentation spans and
  all content attributes.
```

And in the auto-detection paragraph ("The traces edge auto-detects…"), mention OpenCode joins Claude Code and GenAI-semconv sources by instrumentation scope.

- [ ] **Step 2: docs/design.md durable notes**

Add an OpenCode subsection covering: claimed scope (exact `opencode`), the three-span rewrite table from the spec, per-step/per-root granularity consequence (one session trace carries N canonical roots), stateless fragmentation stance, and the structural content policy.

- [ ] **Step 3: harnesses.md relevance note**

In `docs/harnesses.md` "Relevance to the connector", replace the implicit absence with one line noting the native OpenCode path is handled and plugin surfaces remain future work.

- [ ] **Step 4: Component README**

In `connector/codingagentconnector/README.md`, mirror whatever source-listing pattern it uses for codex/cursor/claude/genai, adding the opencode edge.

- [ ] **Step 5: Full unpaid gate**

Run: `./scripts/check.sh`
Expected: every step green, including the new compose validation and image build.

- [ ] **Step 6: Commit**

```bash
git add README.md docs/design.md docs/harnesses.md connector/codingagentconnector/README.md
git commit -m "docs: cover OpenCode native trace normalization"
```
