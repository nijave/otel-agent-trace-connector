# GenAI Semconv Trace Normalization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to execute this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Normalize traces from `opentelemetry-instrumentation-openai-v2`, `opentelemetry-util-genai`, and Strands Agents SDK into the connector's canonical `invoke_agent`/`chat`/`execute_tool` vocabulary.

**Architecture:** A new stateless `internal/genai` normalizer claims resource-spans groups by instrumentation-scope name (Claude groups keep priority), rewrites span names and attributes to the canonical vocabulary, and strips content-bearing attributes and events. A small router in the component root fans the traces-to-traces edge across the Claude and GenAI normalizers.

**Tech Stack:** Go (OpenTelemetry Collector pdata, connector APIs), testify, Docker Compose e2e stacks with Python agents (`openai` SDK, `opentelemetry-instrumentation-openai-v2`, `strands-agents`).

**Spec:** `docs/superpowers/specs/2026-08-20-genai-semconv-traces-design.md`

## Global Constraints

- Work on branch `design/genai-semconv-traces` (already checked out).
- The connector is its own Go module. Run its tests as
  `(cd connector/codingagentconnector && go test -race ./...)`; run root-module
  tests as `go test ./...` from the repo root.
- `Capabilities()` reports `MutatesData: false` on every traces edge: never
  mutate the input batch; copy resource groups before editing.
- Canonical output never carries prompt text, tool arguments, tool output, or
  message content. The stripping lists in Task 3 are the single source of truth.
- Scope allowlist, verbatim from the spec (prefix matching):
  `opentelemetry.instrumentation.openai_v2`, `opentelemetry.util.genai`,
  `opentelemetry.instrumentation.genai`, `strands.telemetry`.
- Provenance values, verbatim: `telemetry.source=native`,
  `coding_agent.source.scope=<instrumentation scope name>`,
  `coding_agent.client.name=<resource service.name>`,
  `coding_agent.client.version=<resource service.version>`.
- z.ai endpoints: OpenAI-compatible chat completions at
  `https://api.z.ai/api/coding/paas/v4`; model default `glm-4.7`.
- Commit style: Conventional Commits (`feat(genai): ...`, `docs: ...`), subject
  at most 72 characters, imperative mood, no body unless a constraint is not
  visible in the diff.
- Markdown files pass a Vale prose hook on write: use active voice, do not
  write the word `TODO`, avoid the word `therefore`.
- Shell scripts must pass `shellcheck` and `bash -n`/`sh -n`.
- Live e2e runs cost real money and stay manual; every task here verifies
  itself with unpaid commands only (`go test`, `docker build`,
  `docker compose config`).

## File Structure

- `connector/codingagentconnector/internal/claude/normalizer.go` — change:
  export `ContainsClaudeSpans` so the GenAI normalizer can defer to Claude.
- `connector/codingagentconnector/internal/genai/normalizer.go` — create:
  scope detection, group claiming, span normalization, content stripping.
- `connector/codingagentconnector/internal/genai/normalizer_test.go` — create.
- `connector/codingagentconnector/traces.go` — create: traces-edge router.
- `connector/codingagentconnector/traces_test.go` — create.
- `connector/codingagentconnector/factory.go` — change: wire the router.
- `e2e/validator/validator.go`, `e2e/validator/live_test.go` — change: new
  agent kinds `openai_adhoc` and `strands`.
- `e2e/validator/validator_test.go` — change: unit tests for the new checks.
- `e2e/openai-adhoc/{Dockerfile,requirements.txt,agent.py,run.sh}` — create.
- `e2e/strands/{Dockerfile,requirements.txt,agent.py,run.sh}` — create.
- `compose.e2e-openai.yaml`, `compose.e2e-strands.yaml` — create.
- `scripts/e2e-openai.sh`, `scripts/e2e-strands.sh` — create.
- `.github/workflows/ci.yml` — change: lint, build, and config-check the new stacks.
- `README.md`, `connector/codingagentconnector/README.md`, `docs/design.md` —
  change: document the new sources.

---

### Task 1: GenAI scope detection and group claiming

**Files:**
- Change: `connector/codingagentconnector/internal/claude/normalizer.go`
- Create: `connector/codingagentconnector/internal/genai/normalizer.go`
- Test: `connector/codingagentconnector/internal/genai/normalizer_test.go`

**Interfaces:**
- Consumes: `claude.ContainsClaudeSpans(ptrace.ResourceSpans) bool` (exported
  in this task from the existing unexported `containsClaudeSpans`).
- Produces: `genai.New(next consumer.Traces) connector.Traces` — stateless
  normalizer; Task 4's router constructs it. Also unexported
  `matchesGenAIScope(name string) bool` used by later tasks in this package.

- [ ] **Step 1: Export the Claude detection helper**

In `connector/codingagentconnector/internal/claude/normalizer.go`, rename
`containsClaudeSpans` to `ContainsClaudeSpans` (both the declaration and its
one call site in `ConsumeTraces`). Update its doc comment:

```go
// ContainsClaudeSpans reports whether any span in the group carries Claude
// Code's native span-name namespace. The GenAI edge also calls this to leave
// Claude groups to the Claude normalizer.
func ContainsClaudeSpans(resourceSpans ptrace.ResourceSpans) bool {
```

- [ ] **Step 2: Run the Claude tests to confirm the rename is complete**

Run: `cd connector/codingagentconnector && go test ./internal/claude/`
Expected: PASS

- [ ] **Step 3: Commit the rename**

```bash
git add connector/codingagentconnector/internal/claude/normalizer.go
git commit -m "refactor(claude): export ContainsClaudeSpans for the GenAI edge"
```

- [ ] **Step 4: Write the failing claiming tests**

Create `connector/codingagentconnector/internal/genai/normalizer_test.go`.
The `traceSink` mirrors the one in the Claude tests (each internal package
keeps its own copy):

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package genai

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer"
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

func TestGenAINormalizerClaimsKnownScopes(t *testing.T) {
	for _, scope := range []string{
		"opentelemetry.instrumentation.openai_v2",
		"opentelemetry.util.genai.handler",
		"opentelemetry.instrumentation.genai_openai",
		"strands.telemetry.tracer",
	} {
		input := ptrace.NewTraces()
		newGroup(input, scope, "chat glm-4.7")
		sink := &traceSink{}
		require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
		require.Len(t, sink.all(), 1, "scope %q must be claimed", scope)
	}
}

func TestGenAINormalizerSkipsUnknownScopesAndClaudeGroups(t *testing.T) {
	input := ptrace.NewTraces()
	// Unknown scope: some application tracer.
	newGroup(input, "my-app", "startup")
	// Claude group: the Claude normalizer owns it even when a GenAI scope is present.
	claudeGroup := input.ResourceSpans().AppendEmpty()
	claudeScope := claudeGroup.ScopeSpans().AppendEmpty()
	claudeScope.Scope().SetName("opentelemetry.util.genai.handler")
	claudeScope.Spans().AppendEmpty().SetName("claude_code.interaction")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	require.Empty(t, sink.all(), "no group may be claimed")
}

func TestGenAINormalizerKeepsWholeClaimedGroupAndInput(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	appScope := rs.ScopeSpans().AppendEmpty()
	appScope.Scope().SetName("my-agent")
	appScope.Spans().AppendEmpty().SetName("run")
	genaiScope := rs.ScopeSpans().AppendEmpty()
	genaiScope.Scope().SetName("opentelemetry.instrumentation.openai_v2")
	genaiScope.Spans().AppendEmpty().SetName("chat glm-4.7")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	require.Len(t, sink.all(), 1)
	output := sink.all()[0].ResourceSpans().At(0)
	require.Equal(t, 2, output.ScopeSpans().Len(), "application scope must survive")
	// The input is not mutated because Collector fan-out may send it elsewhere.
	require.Equal(t, "run", input.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Name())
}
```

- [ ] **Step 5: Run tests to verify they fail**

Run: `cd connector/codingagentconnector && go test ./internal/genai/`
Expected: FAIL (package does not compile: `New` undefined)

- [ ] **Step 6: Write the claiming implementation**

Create `connector/codingagentconnector/internal/genai/normalizer.go`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package genai normalizes GenAI-semantic-convention native traces (the
// opentelemetry-instrumentation-openai-v2 package in both semconv modes,
// direct opentelemetry-util-genai users, and Strands Agents SDK) into the
// canonical coding-agent vocabulary. It is stateless: hierarchy, IDs, kinds,
// and status pass through; only names and attributes change, and
// content-bearing attributes and events never reach canonical output.
package genai

import (
	"context"
	"strings"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/claude"
)

// scopePrefixes lists the instrumentation-scope names this edge claims.
// Prefixes rather than exact names: upstream is renaming
// opentelemetry-instrumentation-openai-v2 to
// opentelemetry-instrumentation-genai-openai, and util-genai emits from a
// module whose path may shift below opentelemetry.util.genai.
var scopePrefixes = []string{
	"opentelemetry.instrumentation.openai_v2",
	"opentelemetry.util.genai",
	"opentelemetry.instrumentation.genai",
	"strands.telemetry",
}

type genAITraceNormalizer struct {
	next consumer.Traces
	component.StartFunc
	component.ShutdownFunc
}

// New creates the stateless GenAI-semconv traces-to-traces edge.
func New(next consumer.Traces) connector.Traces {
	return &genAITraceNormalizer{next: next}
}

func (*genAITraceNormalizer) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (n *genAITraceNormalizer) ConsumeTraces(ctx context.Context, input ptrace.Traces) error {
	output := ptrace.NewTraces()
	for i := 0; i < input.ResourceSpans().Len(); i++ {
		inputResourceSpans := input.ResourceSpans().At(i)
		// Claude groups belong to the Claude normalizer even when they also
		// carry GenAI scopes; claiming here would emit the group twice.
		if claude.ContainsClaudeSpans(inputResourceSpans) {
			continue
		}
		if !containsGenAIScopes(inputResourceSpans) {
			continue
		}
		rs := output.ResourceSpans().AppendEmpty()
		inputResourceSpans.CopyTo(rs)
	}
	if output.SpanCount() == 0 {
		return nil
	}
	return n.next.ConsumeTraces(ctx, output)
}

func containsGenAIScopes(resourceSpans ptrace.ResourceSpans) bool {
	for i := 0; i < resourceSpans.ScopeSpans().Len(); i++ {
		if matchesGenAIScope(resourceSpans.ScopeSpans().At(i).Scope().Name()) {
			return true
		}
	}
	return false
}

func matchesGenAIScope(name string) bool {
	for _, prefix := range scopePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func resourceString(resource pcommon.Resource, key string) string {
	value, ok := resource.Attributes().Get(key)
	if !ok {
		return ""
	}
	return value.Str()
}

var _ connector.Traces = (*genAITraceNormalizer)(nil)
```

`resourceString` gains its caller in Task 2; Go allows uncalled
package-level functions, so the package compiles.

- [ ] **Step 7: Run tests to verify they pass**

Run: `cd connector/codingagentconnector && go test ./internal/genai/ ./internal/claude/`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add connector/codingagentconnector/internal/genai/
git commit -m "feat(genai): claim GenAI-semconv resource groups by scope"
```

---

### Task 2: Span name and attribute normalization

**Files:**
- Change: `connector/codingagentconnector/internal/genai/normalizer.go`
- Test: `connector/codingagentconnector/internal/genai/normalizer_test.go`

**Interfaces:**
- Consumes: the claiming loop from Task 1.
- Produces: unexported `normalizeSpan(span ptrace.Span, scopeName, serviceName,
  serviceVersion string)` called from `ConsumeTraces`; canonical attributes
  listed in Global Constraints.

- [ ] **Step 1: Write the failing normalization tests**

Append to `normalizer_test.go`. The Strands fixture mirrors the real tracer
(`strands.telemetry.tracer` scope, bare `chat` name, `gen_ai.system`,
duplicate token keys); the openai-v2 fixtures mirror both semconv modes:

```go
func TestGenAINormalizerNormalizesOpenAIV2LegacyChat(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "adhoc-agent")
	rs.Resource().Attributes().PutStr("service.version", "0.1.0")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("opentelemetry.instrumentation.openai_v2")
	span := ss.Spans().AppendEmpty()
	span.SetName("chat glm-4.7")
	span.Attributes().PutStr("gen_ai.operation.name", "chat")
	span.Attributes().PutStr("gen_ai.system", "openai")
	span.Attributes().PutStr("gen_ai.request.model", "glm-4.7")
	span.Attributes().PutInt("gen_ai.usage.input_tokens", 12)
	span.Attributes().PutInt("gen_ai.usage.output_tokens", 34)

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, "chat glm-4.7", out.Name())
	require.Equal(t, "openai", attrString(t, out, "gen_ai.provider.name"))
	_, hasSystem := out.Attributes().Get("gen_ai.system")
	require.False(t, hasSystem, "legacy gen_ai.system must not survive")
	require.Equal(t, "native", attrString(t, out, "telemetry.source"))
	require.Equal(t, "opentelemetry.instrumentation.openai_v2",
		attrString(t, out, "coding_agent.source.scope"))
	require.Equal(t, "adhoc-agent", attrString(t, out, "coding_agent.client.name"))
	require.Equal(t, "0.1.0", attrString(t, out, "coding_agent.client.version"))
}

func TestGenAINormalizerKeepsExperimentalProviderName(t *testing.T) {
	input := ptrace.NewTraces()
	span := newGroup(input, "opentelemetry.util.genai.handler", "chat glm-4.7")
	span.Attributes().PutStr("gen_ai.operation.name", "chat")
	span.Attributes().PutStr("gen_ai.provider.name", "openai")
	span.Attributes().PutStr("gen_ai.request.model", "glm-4.7")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, "openai", attrString(t, out, "gen_ai.provider.name"))
}

func TestGenAINormalizerNormalizesStrandsTree(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("strands.telemetry.tracer")

	agent := ss.Spans().AppendEmpty()
	agent.SetName("invoke_agent strands-e2e")
	agent.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
	agent.Attributes().PutStr("gen_ai.system", "strands-agents")
	agent.Attributes().PutStr("gen_ai.agent.name", "strands-e2e")

	cycle := ss.Spans().AppendEmpty()
	cycle.SetName("execute_event_loop_cycle")
	cycle.Attributes().PutStr("gen_ai.operation.name", "execute_event_loop_cycle")
	cycle.Attributes().PutStr("gen_ai.system", "strands-agents")

	chat := ss.Spans().AppendEmpty()
	chat.SetName("chat")
	chat.Attributes().PutStr("gen_ai.operation.name", "chat")
	chat.Attributes().PutStr("gen_ai.system", "strands-agents")
	chat.Attributes().PutStr("gen_ai.request.model", "glm-4.7")
	chat.Attributes().PutInt("gen_ai.usage.prompt_tokens", 10)
	chat.Attributes().PutInt("gen_ai.usage.completion_tokens", 20)
	chat.Attributes().PutInt("gen_ai.usage.input_tokens", 10)
	chat.Attributes().PutInt("gen_ai.usage.output_tokens", 20)
	chat.Attributes().PutInt("gen_ai.usage.total_tokens", 30)

	tool := ss.Spans().AppendEmpty()
	tool.SetName("execute_tool get_marker")
	tool.Attributes().PutStr("gen_ai.operation.name", "execute_tool")
	tool.Attributes().PutStr("gen_ai.system", "strands-agents")
	tool.Attributes().PutStr("gen_ai.tool.name", "get_marker")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans()

	require.Equal(t, "invoke_agent strands-e2e", out.At(0).Name())
	require.Equal(t, "strands-agents", attrString(t, out.At(0), "gen_ai.provider.name"))
	// Non-canonical operations keep their emitted names.
	require.Equal(t, "execute_event_loop_cycle", out.At(1).Name())
	// Bare Strands chat gains the model suffix.
	require.Equal(t, "chat glm-4.7", out.At(2).Name())
	// Legacy token keys are mapped/removed; current keys and totals survive.
	_, hasPrompt := out.At(2).Attributes().Get("gen_ai.usage.prompt_tokens")
	require.False(t, hasPrompt)
	_, hasCompletion := out.At(2).Attributes().Get("gen_ai.usage.completion_tokens")
	require.False(t, hasCompletion)
	require.Equal(t, int64(10), attrInt(t, out.At(2), "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(20), attrInt(t, out.At(2), "gen_ai.usage.output_tokens"))
	require.Equal(t, int64(30), attrInt(t, out.At(2), "gen_ai.usage.total_tokens"))
	require.Equal(t, "execute_tool get_marker", out.At(3).Name())
}

func TestGenAINormalizerMapsLegacyTokensWhenCurrentAbsent(t *testing.T) {
	input := ptrace.NewTraces()
	span := newGroup(input, "strands.telemetry.tracer", "chat")
	span.Attributes().PutStr("gen_ai.operation.name", "chat")
	span.Attributes().PutInt("gen_ai.usage.prompt_tokens", 7)
	span.Attributes().PutInt("gen_ai.usage.completion_tokens", 9)

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, int64(7), attrInt(t, out, "gen_ai.usage.input_tokens"))
	require.Equal(t, int64(9), attrInt(t, out, "gen_ai.usage.output_tokens"))
	// Model attribute is absent, so the emitted name stays.
	require.Equal(t, "chat", out.Name())
}

func TestGenAINormalizerLeavesSpansWithoutOperationName(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	appScope := rs.ScopeSpans().AppendEmpty()
	appScope.Scope().SetName("my-agent")
	appSpan := appScope.Spans().AppendEmpty()
	appSpan.SetName("run")
	genaiScope := rs.ScopeSpans().AppendEmpty()
	genaiScope.Scope().SetName("opentelemetry.instrumentation.openai_v2")
	genaiScope.Spans().AppendEmpty().SetName("chat glm-4.7")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	outApp := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, "run", outApp.Name())
	_, tagged := outApp.Attributes().Get("telemetry.source")
	require.False(t, tagged, "spans outside matched scopes stay untouched")
}

func attrString(t *testing.T, span ptrace.Span, key string) string {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok, "attribute %q missing on span %q", key, span.Name())
	return value.Str()
}

func attrInt(t *testing.T, span ptrace.Span, key string) int64 {
	t.Helper()
	value, ok := span.Attributes().Get(key)
	require.True(t, ok, "attribute %q missing on span %q", key, span.Name())
	return value.Int()
}
```

- [ ] **Step 2: Run tests to verify the new ones fail**

Run: `cd connector/codingagentconnector && go test ./internal/genai/`
Expected: FAIL (missing provenance attributes, `gen_ai.system` survives,
bare `chat` name stays)

- [ ] **Step 3: Write the normalization code**

In `normalizer.go`, extend the claiming loop in `ConsumeTraces` (replace the
body after `inputResourceSpans.CopyTo(rs)`):

```go
		serviceName := resourceString(rs.Resource(), "service.name")
		serviceVersion := resourceString(rs.Resource(), "service.version")
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			ss := rs.ScopeSpans().At(j)
			matched := matchesGenAIScope(ss.Scope().Name())
			if !matched {
				continue
			}
			spans := ss.Spans()
			for k := 0; k < spans.Len(); k++ {
				normalizeSpan(spans.At(k), ss.Scope().Name(), serviceName, serviceVersion)
			}
		}
```

Add below `matchesGenAIScope`:

```go
// nameSubjectByOperation maps canonical operations to the attribute that
// supplies the span-name subject ({operation} {subject}).
var nameSubjectByOperation = map[string]string{
	"chat":         "gen_ai.request.model",
	"invoke_agent": "gen_ai.agent.name",
	"execute_tool": "gen_ai.tool.name",
}

func normalizeSpan(span ptrace.Span, scopeName, serviceName, serviceVersion string) {
	attrs := span.Attributes()
	operationValue, ok := attrs.Get("gen_ai.operation.name")
	if !ok {
		return
	}
	operation := operationValue.Str()
	if subjectKey, canonical := nameSubjectByOperation[operation]; canonical {
		if subjectValue, ok := attrs.Get(subjectKey); ok && subjectValue.Str() != "" {
			span.SetName(operation + " " + subjectValue.Str())
		}
	}
	if _, ok := attrs.Get("gen_ai.provider.name"); !ok {
		if systemValue, ok := attrs.Get("gen_ai.system"); ok {
			// Extract before Put: a map write may invalidate held values.
			provider := systemValue.Str()
			if provider != "" {
				attrs.PutStr("gen_ai.provider.name", provider)
			}
		}
	}
	attrs.Remove("gen_ai.system")
	mapLegacyTokens(attrs, "gen_ai.usage.prompt_tokens", "gen_ai.usage.input_tokens")
	mapLegacyTokens(attrs, "gen_ai.usage.completion_tokens", "gen_ai.usage.output_tokens")
	attrs.PutStr("telemetry.source", "native")
	attrs.PutStr("coding_agent.source.scope", scopeName)
	if serviceName != "" {
		attrs.PutStr("coding_agent.client.name", serviceName)
	}
	if serviceVersion != "" {
		attrs.PutStr("coding_agent.client.version", serviceVersion)
	}
}

// mapLegacyTokens copies a legacy usage attribute onto the current key when
// the current key is absent, then drops the legacy key from canonical output.
func mapLegacyTokens(attrs pcommon.Map, legacyKey, currentKey string) {
	legacyValue, ok := attrs.Get(legacyKey)
	if !ok {
		return
	}
	if _, exists := attrs.Get(currentKey); !exists && legacyValue.Type() == pcommon.ValueTypeInt {
		count := legacyValue.Int()
		attrs.PutInt(currentKey, count)
	}
	attrs.Remove(legacyKey)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd connector/codingagentconnector && go test -race ./internal/genai/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/internal/genai/
git commit -m "feat(genai): normalize GenAI span names and attributes"
```

---

### Task 3: Content stripping

**Files:**
- Change: `connector/codingagentconnector/internal/genai/normalizer.go`
- Test: `connector/codingagentconnector/internal/genai/normalizer_test.go`

**Interfaces:**
- Consumes: the claiming loop from Task 1.
- Produces: unexported `stripContent(span ptrace.Span)` applied to every span
  in a claimed group; the key/event lists below are the spec's stripping
  contract and Task 5's validator mirrors them.

- [ ] **Step 1: Write the failing stripping tests**

Append to `normalizer_test.go`:

```go
func TestGenAINormalizerStripsContentAttributesAndEvents(t *testing.T) {
	input := ptrace.NewTraces()
	span := newGroup(input, "strands.telemetry.tracer", "execute_tool get_marker")
	span.Attributes().PutStr("gen_ai.operation.name", "execute_tool")
	span.Attributes().PutStr("gen_ai.tool.name", "get_marker")
	for _, key := range []string{
		"gen_ai.input.messages", "gen_ai.output.messages",
		"gen_ai.input.messages.ref", "gen_ai.output.messages.ref",
		"gen_ai.system_instructions", "system_prompt",
		"gen_ai.tool.call.arguments", "gen_ai.tool.call.result",
		"gen_ai.tool.definitions", "gen_ai.agent.tools",
		"gen_ai.user.message", "gen_ai.assistant.message",
		"gen_ai.system.message", "gen_ai.tool.message", "gen_ai.choice",
		"gen_ai.choice.message", "gen_ai.choice.tool.result",
	} {
		span.Attributes().PutStr(key, "SENSITIVE")
	}
	for _, name := range []string{
		"gen_ai.client.inference.operation.details",
		"gen_ai.user.message", "gen_ai.assistant.message",
		"gen_ai.system.message", "gen_ai.tool.message", "gen_ai.choice",
		"memory.query", "memory.content",
	} {
		event := span.Events().AppendEmpty()
		event.SetName(name)
		event.Attributes().PutStr("content", "SENSITIVE")
	}
	safeEvent := span.Events().AppendEmpty()
	safeEvent.SetName("gen_ai.tool.decision")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	out := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	for _, key := range contentAttributeKeys {
		_, exists := out.Attributes().Get(key)
		require.False(t, exists, "content attribute %q survived", key)
	}
	require.Equal(t, 1, out.Events().Len(), "only the non-content event survives")
	require.Equal(t, "gen_ai.tool.decision", out.Events().At(0).Name())
	// The input keeps its content: raw fidelity is the raw pipeline's job.
	require.Equal(t, 9, input.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Events().Len())
}

func TestGenAINormalizerStripsContentFromUnmatchedScopesInClaimedGroups(t *testing.T) {
	input := ptrace.NewTraces()
	rs := input.ResourceSpans().AppendEmpty()
	appScope := rs.ScopeSpans().AppendEmpty()
	appScope.Scope().SetName("my-agent")
	appSpan := appScope.Spans().AppendEmpty()
	appSpan.SetName("run")
	appSpan.Attributes().PutStr("gen_ai.input.messages", "SENSITIVE")
	genaiScope := rs.ScopeSpans().AppendEmpty()
	genaiScope.Scope().SetName("opentelemetry.instrumentation.openai_v2")
	genaiScope.Spans().AppendEmpty().SetName("chat glm-4.7")

	sink := &traceSink{}
	require.NoError(t, New(sink).ConsumeTraces(context.Background(), input))
	outApp := sink.all()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	_, exists := outApp.Attributes().Get("gen_ai.input.messages")
	require.False(t, exists, "content must not ride along on unmatched scopes")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd connector/codingagentconnector && go test ./internal/genai/`
Expected: FAIL (`contentAttributeKeys` undefined; content survives)

- [ ] **Step 3: Write the stripping code**

In `normalizer.go`, change the scope loop so stripping covers every span in a
claimed group (normalization stays scope-scoped):

```go
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			ss := rs.ScopeSpans().At(j)
			matched := matchesGenAIScope(ss.Scope().Name())
			spans := ss.Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				if matched {
					normalizeSpan(span, ss.Scope().Name(), serviceName, serviceVersion)
				}
				stripContent(span)
			}
		}
```

Add the lists and the helper:

```go
// contentAttributeKeys are content-bearing span attributes that never reach
// canonical output. They cover current emitters (openai-v2 experimental
// capture modes, Strands latest conventions) plus older Strands layouts and
// the gen_ai_span_attributes_only mode.
var contentAttributeKeys = []string{
	"gen_ai.input.messages",
	"gen_ai.output.messages",
	"gen_ai.input.messages.ref",
	"gen_ai.output.messages.ref",
	"gen_ai.system_instructions",
	"system_prompt",
	"gen_ai.tool.call.arguments",
	"gen_ai.tool.call.result",
	"gen_ai.tool.definitions",
	"gen_ai.agent.tools",
	"gen_ai.user.message",
	"gen_ai.assistant.message",
	"gen_ai.system.message",
	"gen_ai.tool.message",
	"gen_ai.choice",
	"gen_ai.choice.message",
	"gen_ai.choice.tool.result",
}

// contentEventNames are span events removed entirely, attributes included.
var contentEventNames = map[string]bool{
	"gen_ai.client.inference.operation.details": true,
	"gen_ai.user.message":                       true,
	"gen_ai.assistant.message":                  true,
	"gen_ai.system.message":                     true,
	"gen_ai.tool.message":                       true,
	"gen_ai.choice":                             true,
	"memory.query":                              true,
	"memory.content":                            true,
}

func stripContent(span ptrace.Span) {
	for _, key := range contentAttributeKeys {
		span.Attributes().Remove(key)
	}
	span.Events().RemoveIf(func(event ptrace.SpanEvent) bool {
		return contentEventNames[event.Name()]
	})
}
```

- [ ] **Step 4: Run the module tests including race**

Run: `cd connector/codingagentconnector && go test -race ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/internal/genai/
git commit -m "feat(genai): strip content attributes and events from canonical output"
```

---

### Task 4: Route the traces edge across Claude and GenAI

**Files:**
- Create: `connector/codingagentconnector/traces.go`
- Create: `connector/codingagentconnector/traces_test.go`
- Change: `connector/codingagentconnector/factory.go`

**Interfaces:**
- Consumes: `claude.New(next) connector.Traces`, `genai.New(next)
  connector.Traces`.
- Produces: unexported `newTracesRouter(next consumer.Traces)
  connector.Traces`, returned by `createTracesToTraces`.

- [ ] **Step 1: Write the failing router test**

Create `connector/codingagentconnector/traces_test.go`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

type routerSink struct {
	mu     sync.Mutex
	traces []ptrace.Traces
}

func (*routerSink) Capabilities() consumer.Capabilities { return consumer.Capabilities{} }
func (s *routerSink) ConsumeTraces(_ context.Context, traces ptrace.Traces) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := ptrace.NewTraces()
	traces.CopyTo(copied)
	s.traces = append(s.traces, copied)
	return nil
}

func TestTracesRouterSendsEachGroupToExactlyOneNormalizer(t *testing.T) {
	input := ptrace.NewTraces()

	claudeGroup := input.ResourceSpans().AppendEmpty()
	claudeGroup.ScopeSpans().AppendEmpty().Spans().AppendEmpty().SetName("claude_code.interaction")

	genaiGroup := input.ResourceSpans().AppendEmpty()
	genaiScope := genaiGroup.ScopeSpans().AppendEmpty()
	genaiScope.Scope().SetName("opentelemetry.instrumentation.openai_v2")
	chat := genaiScope.Spans().AppendEmpty()
	chat.SetName("chat glm-4.7")
	chat.Attributes().PutStr("gen_ai.operation.name", "chat")

	unknownGroup := input.ResourceSpans().AppendEmpty()
	unknownGroup.ScopeSpans().AppendEmpty().Spans().AppendEmpty().SetName("startup")

	sink := &routerSink{}
	router := newTracesRouter(sink)
	require.NoError(t, router.ConsumeTraces(context.Background(), input))

	names := map[string]int{}
	total := 0
	for _, traces := range sink.traces {
		for i := 0; i < traces.ResourceSpans().Len(); i++ {
			rs := traces.ResourceSpans().At(i)
			for j := 0; j < rs.ScopeSpans().Len(); j++ {
				spans := rs.ScopeSpans().At(j).Spans()
				for k := 0; k < spans.Len(); k++ {
					names[spans.At(k).Name()]++
					total++
				}
			}
		}
	}
	require.Equal(t, 2, total, "unknown groups stay out of the canonical edge")
	require.Equal(t, 1, names["invoke_agent claude_code"], "claude normalizer claimed its group once")
	require.Equal(t, 1, names["chat glm-4.7"], "genai normalizer claimed its group once")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd connector/codingagentconnector && go test -run TestTracesRouter ./...`
Expected: FAIL (`newTracesRouter` undefined)

- [ ] **Step 3: Write the router and wire the factory**

Create `connector/codingagentconnector/traces.go`:

```go
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/claude"
	"github.com/nijave/otel-agent-trace-connector/connector/codingagentconnector/internal/genai"
)

// tracesRouter fans the traces-to-traces edge across the stateless
// normalizers. Each normalizer claims disjoint resource groups (the GenAI
// edge defers to Claude via claude.ContainsClaudeSpans), so a group is
// emitted at most once and unclaimed groups stay out of the canonical edge.
type tracesRouter struct {
	edges []connector.Traces
	component.StartFunc
	component.ShutdownFunc
}

func newTracesRouter(next consumer.Traces) connector.Traces {
	return &tracesRouter{edges: []connector.Traces{claude.New(next), genai.New(next)}}
}

func (*tracesRouter) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (r *tracesRouter) ConsumeTraces(ctx context.Context, traces ptrace.Traces) error {
	for _, edge := range r.edges {
		if err := edge.ConsumeTraces(ctx, traces); err != nil {
			return err
		}
	}
	return nil
}

var _ connector.Traces = (*tracesRouter)(nil)
```

In `factory.go`, change `createTracesToTraces` to return the router and drop
the now-unused `claude` import:

```go
func createTracesToTraces(
	_ context.Context,
	_ connector.Settings,
	_ component.Config,
	next consumer.Traces,
) (connector.Traces, error) {
	return newTracesRouter(next), nil
}
```

- [ ] **Step 4: Run the full connector module suite**

Run: `cd connector/codingagentconnector && go test -race ./...`
Expected: PASS (includes the generated component lifecycle tests against the
router)

- [ ] **Step 5: Commit**

```bash
git add connector/codingagentconnector/traces.go connector/codingagentconnector/traces_test.go connector/codingagentconnector/factory.go
git commit -m "feat: route traces edge across claude and genai normalizers"
```

---

### Task 5: Validator support for openai_adhoc and strands agents

**Files:**
- Change: `e2e/validator/validator.go`
- Change: `e2e/validator/live_test.go`
- Test: `e2e/validator/validator_test.go`

**Interfaces:**
- Consumes: existing `collectRunSpans`, `firstValidRoot`, `stringAttr`,
  `validateTraceFile` helpers in `validator.go`.
- Produces: `validateCanonicalTraces` accepting agents `openai_adhoc` and
  `strands`; new `validateStrandsRawFile(path, runID string) error` used by
  `live_test.go`. This task pins the e2e service names that Tasks 6-7 reuse:
  `openai-adhoc-legacy`, `openai-adhoc-latest`; Strands agent name
  `strands-e2e`, tool name `get_marker`.

- [ ] **Step 1: Read the existing unit tests to match their style**

Read `e2e/validator/validator_test.go` before writing. Reuse its fixture
helpers where they exist; the code below assumes plain pdata construction
like the connector tests.

- [ ] **Step 2: Write the failing unit tests**

Append to `e2e/validator/validator_test.go` (adjust construction helpers to
the file's existing style):

```go
func TestValidateOpenAIAdhocCanonical(t *testing.T) {
	traces := ptrace.NewTraces()
	for _, service := range []string{"openai-adhoc-legacy", "openai-adhoc-latest"} {
		rs := traces.ResourceSpans().AppendEmpty()
		rs.Resource().Attributes().PutStr("e2e.run.id", "run-1")
		span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
		span.SetName("chat glm-4.7")
		span.Attributes().PutStr("gen_ai.operation.name", "chat")
		span.Attributes().PutStr("gen_ai.provider.name", "openai")
		span.Attributes().PutStr("telemetry.source", "native")
		span.Attributes().PutStr("coding_agent.client.name", service)
		span.Attributes().PutInt("gen_ai.usage.input_tokens", 5)
		span.Attributes().PutInt("gen_ai.usage.output_tokens", 6)
	}
	require.NoError(t, validateCanonicalTraces(traces, "run-1", "openai_adhoc"))

	// A surviving legacy gen_ai.system must fail validation.
	traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).
		Attributes().PutStr("gen_ai.system", "openai")
	require.Error(t, validateCanonicalTraces(traces, "run-1", "openai_adhoc"))
}

func TestValidateStrandsCanonicalRequiresTreeWithoutContent(t *testing.T) {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("e2e.run.id", "run-1")
	spans := rs.ScopeSpans().AppendEmpty().Spans()

	traceID := pcommon.TraceID{1}
	root := spans.AppendEmpty()
	root.SetName("invoke_agent strands-e2e")
	root.SetTraceID(traceID)
	root.SetSpanID(pcommon.SpanID{2})
	root.Attributes().PutStr("gen_ai.operation.name", "invoke_agent")
	root.Attributes().PutStr("gen_ai.provider.name", "strands-agents")
	root.Attributes().PutStr("telemetry.source", "native")

	chat := spans.AppendEmpty()
	chat.SetName("chat glm-4.7")
	chat.SetTraceID(traceID)
	chat.SetSpanID(pcommon.SpanID{3})
	chat.Attributes().PutStr("gen_ai.operation.name", "chat")
	chat.Attributes().PutStr("gen_ai.request.model", "glm-4.7")
	chat.Attributes().PutInt("gen_ai.usage.input_tokens", 5)

	tool := spans.AppendEmpty()
	tool.SetName("execute_tool get_marker")
	tool.SetTraceID(traceID)
	tool.SetSpanID(pcommon.SpanID{4})
	tool.Attributes().PutStr("gen_ai.operation.name", "execute_tool")
	tool.Attributes().PutStr("gen_ai.tool.name", "get_marker")

	require.NoError(t, validateCanonicalTraces(traces, "run-1", "strands"))

	// Content events must fail canonical validation.
	chat.Events().AppendEmpty().SetName("gen_ai.user.message")
	require.Error(t, validateCanonicalTraces(traces, "run-1", "strands"))
}

func TestValidateStrandsRawRequiresContentEvidence(t *testing.T) {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("e2e.run.id", "run-1")
	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("chat")
	require.Error(t, validateStrandsRawTraces(traces, "run-1"),
		"raw output without content events makes the stripping assertion vacuous")
	span.Events().AppendEmpty().SetName("gen_ai.user.message")
	require.NoError(t, validateStrandsRawTraces(traces, "run-1"))
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./e2e/validator/`
Expected: FAIL (unsupported agents, `validateStrandsRawTraces` undefined)

- [ ] **Step 4: Write the validator changes**

In `e2e/validator/validator.go`:

Add an agent dispatch at the top of `validateCanonicalTraces`, after the
existing `rejectSensitiveAttrs` call:

```go
	switch agent {
	case "openai_adhoc":
		if err := rejectGenAIContent(spans); err != nil {
			return err
		}
		return validateOpenAIAdhocSpans(spans)
	case "strands":
		if err := rejectGenAIContent(spans); err != nil {
			return err
		}
		return validateStrandsSpans(spans)
	}
```

Add the new functions:

```go
// genAIContentAttributeKeys and genAIContentEventNames mirror the stripping
// contract in internal/genai; canonical output must never carry them.
var genAIContentAttributeKeys = []string{
	"gen_ai.input.messages", "gen_ai.output.messages",
	"gen_ai.system_instructions", "system_prompt",
	"gen_ai.tool.call.arguments", "gen_ai.tool.call.result",
	"gen_ai.user.message", "gen_ai.assistant.message", "gen_ai.choice",
	"gen_ai.system",
}

var genAIContentEventNames = []string{
	"gen_ai.client.inference.operation.details",
	"gen_ai.user.message", "gen_ai.assistant.message",
	"gen_ai.system.message", "gen_ai.tool.message", "gen_ai.choice",
}

func rejectGenAIContent(spans []ptrace.Span) error {
	for _, span := range spans {
		for _, key := range genAIContentAttributeKeys {
			if _, ok := span.Attributes().Get(key); ok {
				return fmt.Errorf("attribute %q survived normalization on span %q", key, span.Name())
			}
		}
		for i := 0; i < span.Events().Len(); i++ {
			name := span.Events().At(i).Name()
			for _, banned := range genAIContentEventNames {
				if name == banned {
					return fmt.Errorf("content event %q survived normalization on span %q", name, span.Name())
				}
			}
		}
	}
	return nil
}

// validateOpenAIAdhocSpans requires one normalized chat span per semconv
// mode; run.sh runs the agent twice under these two service names.
func validateOpenAIAdhocSpans(spans []ptrace.Span) error {
	for _, service := range []string{"openai-adhoc-legacy", "openai-adhoc-latest"} {
		if err := validateAdhocChat(spans, service); err != nil {
			return err
		}
	}
	return nil
}

func validateAdhocChat(spans []ptrace.Span, service string) error {
	var lastErr error
	for _, span := range spans {
		if stringAttr(span, "coding_agent.client.name") != service ||
			stringAttr(span, "gen_ai.operation.name") != "chat" {
			continue
		}
		if stringAttr(span, "gen_ai.provider.name") != "openai" {
			lastErr = fmt.Errorf("%s: chat provider is not openai", service)
			continue
		}
		if stringAttr(span, "telemetry.source") != "native" {
			lastErr = fmt.Errorf("%s: telemetry source is not native", service)
			continue
		}
		if _, ok := span.Attributes().Get("gen_ai.usage.input_tokens"); !ok {
			lastErr = fmt.Errorf("%s: chat input token usage is missing", service)
			continue
		}
		if _, ok := span.Attributes().Get("gen_ai.usage.output_tokens"); !ok {
			lastErr = fmt.Errorf("%s: chat output token usage is missing", service)
			continue
		}
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("no normalized chat span for service %q", service)
}

// validateStrandsSpans checks names within the root's trace rather than
// direct parentage: Strands nests chat and tool spans under
// execute_event_loop_cycle, so the canonical children are descendants.
func validateStrandsSpans(spans []ptrace.Span) error {
	return firstValidRoot(spans, "invoke_agent strands-e2e", "strands root span was not found", func(root ptrace.Span) error {
		if root.ParentSpanID() != [8]byte{} {
			return errors.New("strands root unexpectedly has a parent")
		}
		if stringAttr(root, "gen_ai.provider.name") != "strands-agents" {
			return errors.New("strands provider is not strands-agents")
		}
		if stringAttr(root, "telemetry.source") != "native" {
			return errors.New("strands telemetry source is not native")
		}
		chat, tool := false, false
		for _, span := range spans {
			if span.TraceID() != root.TraceID() {
				continue
			}
			switch stringAttr(span, "gen_ai.operation.name") {
			case "chat":
				if stringAttr(span, "gen_ai.request.model") == "" {
					return errors.New("strands chat model is missing")
				}
				if _, ok := span.Attributes().Get("gen_ai.usage.input_tokens"); !ok {
					return errors.New("strands chat input token usage is missing")
				}
				chat = true
			case "execute_tool":
				if stringAttr(span, "gen_ai.tool.name") == "get_marker" {
					tool = true
				}
			}
		}
		if !chat {
			return errors.New("strands chat span is missing")
		}
		if !tool {
			return errors.New("strands get_marker tool span is missing")
		}
		return nil
	})
}

// validateStrandsRawFile proves the stripping assertion is not vacuous: the
// raw export must still hold at least one content event.
func validateStrandsRawFile(path, runID string) error {
	return validateTraceFile(path, runID, validateStrandsRawTraces)
}

func validateStrandsRawTraces(traces ptrace.Traces, runID string) error {
	spans := collectRunSpans(traces, runID)
	if len(spans) == 0 {
		return errors.New("strands run id was not found in raw output")
	}
	for _, span := range spans {
		for i := 0; i < span.Events().Len(); i++ {
			switch span.Events().At(i).Name() {
			case "gen_ai.user.message", "gen_ai.choice", "gen_ai.client.inference.operation.details":
				return nil
			}
		}
	}
	return errors.New("raw strands output holds no content events")
}
```

In `e2e/validator/live_test.go`, replace the agent allowlist and the raw
validation section:

```go
	switch agent {
	case "codex", "claude_code", "openai_adhoc", "strands":
	default:
		t.Fatalf("unsupported E2E_AGENT %q", agent)
	}
	rawPath := os.Getenv("RAW_TRACE_FILE")
	if (agent == "claude_code" || agent == "strands") && rawPath == "" {
		t.Fatal("RAW_TRACE_FILE is required for this agent's validation")
	}
```

and in the poll loop:

```go
		lastErr = validateCanonicalFile(path, runID, agent)
		if lastErr == nil && agent == "claude_code" {
			lastErr = validateClaudeRawFile(rawPath, runID)
		}
		if lastErr == nil && agent == "strands" {
			lastErr = validateStrandsRawFile(rawPath, runID)
		}
```

- [ ] **Step 5: Run the validator tests, including the e2e-tag compile check**

Run: `go test ./e2e/validator/ && go test -tags=e2e ./e2e/validator/`
Expected: PASS (the e2e-tagged test skips without `E2E_RUN_ID`)

- [ ] **Step 6: Commit**

```bash
git add e2e/validator/
git commit -m "test(e2e): validate openai-adhoc and strands canonical traces"
```

---

### Task 6: openai-adhoc live E2E stack

**Files:**
- Create: `e2e/openai-adhoc/Dockerfile`
- Create: `e2e/openai-adhoc/requirements.txt`
- Create: `e2e/openai-adhoc/agent.py`
- Create: `e2e/openai-adhoc/run.sh`
- Create: `compose.e2e-openai.yaml`
- Create: `scripts/e2e-openai.sh`

**Interfaces:**
- Consumes: `scripts/lib-e2e.sh` (`compose_files`, `support_services`,
  `e2e_run`); validator agent kind `openai_adhoc` and service names
  `openai-adhoc-legacy` / `openai-adhoc-latest` from Task 5.
- Produces: a paid, manual stack run via `scripts/e2e-openai.sh`; CI (Task 8)
  only builds and validates it.

- [ ] **Step 1: Create the agent script**

`e2e/openai-adhoc/agent.py`:

```python
"""Minimal ad-hoc agent: one chat completion against z.ai's OpenAI-compatible
endpoint, instrumented by opentelemetry-instrumentation-openai-v2. run.sh
executes this once per semconv mode; OTEL_SERVICE_NAME and
OTEL_SEMCONV_STABILITY_OPT_IN select the mode per process."""

import os

from openai import OpenAI
from opentelemetry import trace
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.instrumentation.openai_v2 import OpenAIInstrumentor
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor


def main() -> None:
    provider = TracerProvider()  # service.name comes from OTEL_SERVICE_NAME
    provider.add_span_processor(BatchSpanProcessor(OTLPSpanExporter()))
    trace.set_tracer_provider(provider)
    OpenAIInstrumentor().instrument(tracer_provider=provider)

    client = OpenAI(base_url=os.environ["OPENAI_BASE_URL"])
    response = client.chat.completions.create(
        model=os.environ.get("E2E_OPENAI_MODEL", "glm-4.7"),
        messages=[{"role": "user", "content": "Reply with only: openai-otel-e2e"}],
        max_tokens=16,
    )
    print(response.choices[0].message.content)
    provider.force_flush()
    provider.shutdown()


if __name__ == "__main__":
    main()
```

- [ ] **Step 2: Create the runner**

`e2e/openai-adhoc/run.sh`:

```sh
#!/bin/sh
set -eu

if [ -z "${OPENAI_API_KEY:-}" ]; then
  echo "OPENAI_API_KEY (your z.ai API key) is required; this test runs a real paid model." >&2
  exit 2
fi

# Default mode: semconv v1.30.0 (gen_ai.system on spans).
OTEL_SERVICE_NAME=openai-adhoc-legacy \
  timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" python /work/agent.py

# Experimental mode: semconv v1.37 via opentelemetry-util-genai
# (gen_ai.provider.name on spans).
OTEL_SERVICE_NAME=openai-adhoc-latest \
OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental \
  timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" python /work/agent.py
```

- [ ] **Step 3: Create requirements with real pins**

Resolve current versions first (network step, run on the host):

```bash
docker run --rm python:3.13-slim sh -c \
  "pip index versions openai 2>/dev/null; pip index versions opentelemetry-instrumentation-openai-v2 2>/dev/null"
```

Write `e2e/openai-adhoc/requirements.txt` using the latest stable `openai`
2.x and the latest `opentelemetry-instrumentation-openai-v2` beta the command
reports (2.4b0 at spec-research time, 2026-08-19); let pip resolve the
matching SDK and exporter, then freeze to exact pins in Step 5:

```text
openai==<latest stable 2.x from the command above>
opentelemetry-instrumentation-openai-v2==<latest from the command above>
opentelemetry-exporter-otlp-proto-http
```

- [ ] **Step 4: Create the Dockerfile**

`e2e/openai-adhoc/Dockerfile`:

```dockerfile
FROM python:3.13-slim

COPY requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir --requirement /tmp/requirements.txt

RUN mkdir -p /work
COPY agent.py /work/agent.py
COPY run.sh /usr/local/bin/run-openai-e2e
RUN chmod 0555 /usr/local/bin/run-openai-e2e

WORKDIR /work
ENTRYPOINT ["/usr/local/bin/run-openai-e2e"]
```

- [ ] **Step 5: Build, then freeze the resolved set into exact pins**

```bash
docker build --tag openai-adhoc-e2e:dev e2e/openai-adhoc
docker run --rm --entrypoint pip openai-adhoc-e2e:dev freeze > e2e/openai-adhoc/requirements.txt
docker build --tag openai-adhoc-e2e:dev e2e/openai-adhoc
```

Expected: both builds succeed; `requirements.txt` now holds exact `==` pins
for every transitive dependency.

- [ ] **Step 6: Create the compose file**

`compose.e2e-openai.yaml`:

```yaml
# Ad-hoc OpenAI-SDK agent e2e stack. The shared collector comes from
# compose.e2e-base.yaml. The agent is a small Python script instrumented by
# opentelemetry-instrumentation-openai-v2, pointed at z.ai's OpenAI-compatible
# chat-completions endpoint. It receives exactly one credential.
include:
  - compose.e2e-base.yaml

services:
  agent:
    build:
      context: e2e/openai-adhoc
    environment:
      # scripts/e2e-openai.sh checks this credential before Compose runs.
      OPENAI_API_KEY: "${OPENAI_API_KEY:-}"
      OPENAI_BASE_URL: "https://api.z.ai/api/coding/paas/v4"
      E2E_OPENAI_MODEL: "${E2E_OPENAI_MODEL:-glm-4.7}"
      E2E_AGENT_TIMEOUT: "${E2E_AGENT_TIMEOUT:-10m}"
      E2E_RUN_ID: "${E2E_RUN_ID:?set E2E_RUN_ID or run scripts/e2e-openai.sh}"
      OTEL_EXPORTER_OTLP_ENDPOINT: http://collector:4318
      OTEL_EXPORTER_OTLP_PROTOCOL: http/protobuf
    depends_on:
      collector:
        condition: service_healthy
```

The collector's `resource/e2e` processor stamps `e2e.run.id` on everything it
receives, so the agent needs no `OTEL_RESOURCE_ATTRIBUTES` marker.

- [ ] **Step 7: Create the orchestration script**

`scripts/e2e-openai.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

# The container receives one credential: the z.ai API key, used directly by
# the openai SDK against z.ai's OpenAI-compatible endpoint.
if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "OPENAI_API_KEY (your z.ai API key) is required; this test runs a real paid model." >&2
  exit 2
fi

# Selects the openai-adhoc validation path in the shared validator.
export E2E_AGENT=openai_adhoc

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib-e2e.sh
. "${script_dir}/lib-e2e.sh"

compose_files=(-f compose.e2e-openai.yaml)
support_services=(collector)
e2e_run openai
```

Make it executable: `chmod +x scripts/e2e-openai.sh`

- [ ] **Step 8: Check the stack without paying**

```bash
shellcheck scripts/e2e-openai.sh e2e/openai-adhoc/run.sh
bash -n scripts/e2e-openai.sh && sh -n e2e/openai-adhoc/run.sh
E2E_RUN_ID=local-validation OPENAI_API_KEY=validation-only \
  docker compose -f compose.e2e-openai.yaml config --quiet
```

Expected: all commands exit 0.

- [ ] **Step 9: Commit**

```bash
git add e2e/openai-adhoc/ compose.e2e-openai.yaml scripts/e2e-openai.sh
git commit -m "feat(e2e): add openai-adhoc live stack"
```

---

### Task 7: Strands live E2E stack

**Files:**
- Create: `e2e/strands/Dockerfile`
- Create: `e2e/strands/requirements.txt`
- Create: `e2e/strands/agent.py`
- Create: `e2e/strands/run.sh`
- Create: `compose.e2e-strands.yaml`
- Create: `scripts/e2e-strands.sh`

**Interfaces:**
- Consumes: `scripts/lib-e2e.sh`; validator agent kind `strands`, agent name
  `strands-e2e`, and tool name `get_marker` from Task 5.
- Produces: a paid, manual stack run via `scripts/e2e-strands.sh`.

- [ ] **Step 1: Create the agent script**

`e2e/strands/agent.py`:

```python
"""Minimal Strands agent with one tool, exporting Strands' native OTel traces
to the shared collector. Runs in the default (legacy semconv) mode, which is
what ad-hoc Strands agents emit unless they opt in to experimental semconv."""

import os

from strands import Agent, tool
from strands.models.openai import OpenAIModel
from strands.telemetry import StrandsTelemetry


@tool
def get_marker() -> str:
    """Return the fixed e2e marker string."""
    return "strands-otel-e2e"


def main() -> None:
    StrandsTelemetry().setup_otlp_exporter()  # reads OTEL_EXPORTER_OTLP_ENDPOINT
    model = OpenAIModel(
        client_args={
            "api_key": os.environ["OPENAI_API_KEY"],
            "base_url": os.environ["OPENAI_BASE_URL"],
        },
        model_id=os.environ.get("E2E_STRANDS_MODEL", "glm-4.7"),
        params={"max_tokens": 250},
    )
    agent = Agent(
        name="strands-e2e",
        model=model,
        tools=[get_marker],
        callback_handler=None,
    )
    result = agent("Call the get_marker tool exactly once, then reply with only: done.")
    print(result)


if __name__ == "__main__":
    main()
```

If the pinned Strands release rejects the `name` keyword or the
`strands.models.openai` import path, check
`https://strandsagents.com/docs/user-guide/concepts/model-providers/openai/`
for the current constructor and adjust; the agent name must stay
`strands-e2e` because the validator asserts `invoke_agent strands-e2e`.

- [ ] **Step 2: Create the runner**

`e2e/strands/run.sh`:

```sh
#!/bin/sh
set -eu

if [ -z "${OPENAI_API_KEY:-}" ]; then
  echo "OPENAI_API_KEY (your z.ai API key) is required; this test runs a real paid model." >&2
  exit 2
fi

exec timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" python /work/agent.py
```

- [ ] **Step 3: Create requirements with real pins**

Resolve the current version, then follow the same freeze flow as Task 6:

```bash
docker run --rm python:3.13-slim sh -c "pip index versions 'strands-agents' 2>/dev/null"
```

Initial `e2e/strands/requirements.txt`:

```text
strands-agents[openai,otel]==<latest stable from the command above>
```

If the `otel` extra does not exist in the pinned release, add
`opentelemetry-exporter-otlp-proto-http` as a second line instead; the
Dockerfile build in Step 5 fails loudly on a wrong extra name.

- [ ] **Step 4: Create the Dockerfile**

`e2e/strands/Dockerfile`:

```dockerfile
FROM python:3.13-slim

COPY requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir --requirement /tmp/requirements.txt

RUN mkdir -p /work
COPY agent.py /work/agent.py
COPY run.sh /usr/local/bin/run-strands-e2e
RUN chmod 0555 /usr/local/bin/run-strands-e2e

WORKDIR /work
ENTRYPOINT ["/usr/local/bin/run-strands-e2e"]
```

- [ ] **Step 5: Build, freeze pins, rebuild**

```bash
docker build --tag strands-e2e:dev e2e/strands
docker run --rm --entrypoint pip strands-e2e:dev freeze > e2e/strands/requirements.txt
docker build --tag strands-e2e:dev e2e/strands
```

Expected: both builds succeed; exact pins committed.

- [ ] **Step 6: Create the compose file**

`compose.e2e-strands.yaml`:

```yaml
# Strands Agents SDK e2e stack. The shared collector comes from
# compose.e2e-base.yaml. Strands exports its native OTel traces directly; its
# OpenAI-compatible model provider is pointed at z.ai. The container receives
# exactly one credential. Strands captures prompt/completion content in span
# events by default; the raw pipeline retains it and the validator asserts the
# canonical pipeline strips it.
include:
  - compose.e2e-base.yaml

services:
  agent:
    build:
      context: e2e/strands
    environment:
      # scripts/e2e-strands.sh checks this credential before Compose runs.
      OPENAI_API_KEY: "${OPENAI_API_KEY:-}"
      OPENAI_BASE_URL: "https://api.z.ai/api/coding/paas/v4"
      E2E_STRANDS_MODEL: "${E2E_STRANDS_MODEL:-glm-4.7}"
      E2E_AGENT_TIMEOUT: "${E2E_AGENT_TIMEOUT:-10m}"
      E2E_RUN_ID: "${E2E_RUN_ID:?set E2E_RUN_ID or run scripts/e2e-strands.sh}"
      OTEL_EXPORTER_OTLP_ENDPOINT: http://collector:4318
      OTEL_EXPORTER_OTLP_PROTOCOL: http/protobuf
    depends_on:
      collector:
        condition: service_healthy
```

- [ ] **Step 7: Create the orchestration script**

`scripts/e2e-strands.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

# The container receives one credential: the z.ai API key, used by Strands'
# OpenAI-compatible model provider.
if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "OPENAI_API_KEY (your z.ai API key) is required; this test runs a real paid model." >&2
  exit 2
fi

# Selects the strands validation path in the shared validator.
export E2E_AGENT=strands

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib-e2e.sh
. "${script_dir}/lib-e2e.sh"

compose_files=(-f compose.e2e-strands.yaml)
support_services=(collector)
e2e_run strands
```

Make it executable: `chmod +x scripts/e2e-strands.sh`

- [ ] **Step 8: Check the stack without paying**

```bash
shellcheck scripts/e2e-strands.sh e2e/strands/run.sh
bash -n scripts/e2e-strands.sh && sh -n e2e/strands/run.sh
E2E_RUN_ID=local-validation OPENAI_API_KEY=validation-only \
  docker compose -f compose.e2e-strands.yaml config --quiet
```

Expected: all commands exit 0.

- [ ] **Step 9: Commit**

```bash
git add e2e/strands/ compose.e2e-strands.yaml scripts/e2e-strands.sh
git commit -m "feat(e2e): add strands live stack"
```

---

### Task 8: CI coverage for the new stacks

**Files:**
- Change: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: the stacks and scripts from Tasks 6-7.
- Produces: CI lint, compose validation, credential-split assertions, and
  image builds; no paid runs.

- [ ] **Step 1: Extend the shell lint lists**

In the shell-syntax step, extend both commands:

```yaml
          bash -n scripts/e2e.sh scripts/e2e-claude.sh scripts/e2e-openai.sh scripts/e2e-strands.sh scripts/generate.sh scripts/lib-e2e.sh
          sh -n e2e/codex/run.sh e2e/claude/run.sh e2e/openai-adhoc/run.sh e2e/strands/run.sh
```

and the shellcheck step:

```yaml
        run: shellcheck scripts/e2e.sh scripts/e2e-claude.sh scripts/e2e-openai.sh scripts/e2e-strands.sh scripts/generate.sh scripts/lib-e2e.sh e2e/codex/run.sh e2e/claude/run.sh e2e/openai-adhoc/run.sh e2e/strands/run.sh
```

- [ ] **Step 2: Extend the Compose validation step**

Append to the `Validate Compose configurations` run block (its `env` already
sets `E2E_RUN_ID` and `OPENAI_API_KEY: validation-only`):

```yaml
          # Each new stack receives exactly one credential: the z.ai key as
          # OPENAI_API_KEY. No Anthropic credential may leak in.
          docker compose -f compose.e2e-openai.yaml config --quiet
          docker compose -f compose.e2e-openai.yaml config --format json \
            | jq -e '.services.agent.environment.OPENAI_API_KEY == "validation-only"
                     and (.services.agent.environment | has("ANTHROPIC_AUTH_TOKEN") | not)'
          docker compose -f compose.e2e-strands.yaml config --quiet
          docker compose -f compose.e2e-strands.yaml config --format json \
            | jq -e '.services.agent.environment.OPENAI_API_KEY == "validation-only"
                     and (.services.agent.environment | has("ANTHROPIC_AUTH_TOKEN") | not)'
```

- [ ] **Step 3: Extend the image build step**

Append to the `Build container images` run block:

```yaml
          docker build --tag openai-adhoc-e2e:ci e2e/openai-adhoc
          docker build --tag strands-e2e:ci e2e/strands
```

- [ ] **Step 4: Check the workflow locally**

```bash
python3 -c "import yaml, sys; yaml.safe_load(open('.github/workflows/ci.yml'))"
E2E_RUN_ID=ci-validation OPENAI_API_KEY=validation-only \
  docker compose -f compose.e2e-openai.yaml config --format json \
  | jq -e '.services.agent.environment.OPENAI_API_KEY == "validation-only"'
```

Expected: exit 0 for both.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: lint, validate, and build the new e2e stacks"
```

---

### Task 9: Documentation

**Files:**
- Change: `README.md`
- Change: `connector/codingagentconnector/README.md`
- Change: `docs/design.md`

A Vale prose hook lints these files on write: keep sentences active, avoid
the word `therefore`, and do not write the word `TODO`.

- [ ] **Step 1: Update the root README**

In the intro list (after the Claude Code bullet), add:

```markdown
- **GenAI semconv sources:** normalizes native traces from
  `opentelemetry-instrumentation-openai-v2` (both semconv modes),
  direct `opentelemetry-util-genai` users, and the Strands Agents SDK into
  the same canonical vocabulary, stripping prompt/completion/tool content
  from canonical output.
```

In the "Collector configuration" section, update the sentence describing the
traces pipeline to state that the traces edge auto-detects Claude Code and
GenAI-semconv sources by instrumentation scope, and add after the Claude Code
paragraph:

```markdown
Ad-hoc Python agents can export through
[`opentelemetry-instrumentation-openai-v2`](https://pypi.org/project/opentelemetry-instrumentation-openai-v2/)
or `opentelemetry-util-genai`; Strands Agents SDK exports its
[built-in traces](https://strandsagents.com/docs/user-guide/observability-evaluation/traces/)
directly. All of them enter the same `traces` pipeline as Claude Code — the
connector detects each source by instrumentation scope.

Strands captures prompt and completion content in span events by default and
its redaction is opt-in, so the raw trace destination receives content under
default agent settings. Configure Strands redaction
(`gen_ai_unredacted_attributes` in `OTEL_SEMCONV_STABILITY_OPT_IN`) or apply
the same access policy to the raw destination as to any content store. The
canonical pipeline strips content-bearing attributes and events regardless.
```

Add to References:

```markdown
- [opentelemetry-instrumentation-openai-v2](https://github.com/open-telemetry/opentelemetry-python-contrib/tree/main/instrumentation-genai/opentelemetry-instrumentation-openai-v2)
- [Strands Agents traces](https://strandsagents.com/docs/user-guide/observability-evaluation/traces/)
```

Update the Tests section sentence listing suite coverage to mention GenAI
normalization, and mention the two new opt-in E2Es alongside the existing
ones.

- [ ] **Step 2: Update the connector README**

After the Claude Code bullet in the edge list, add:

```markdown
- **GenAI semconv (traces → traces):** claims resource groups whose
  instrumentation scope starts with `opentelemetry.instrumentation.openai_v2`,
  `opentelemetry.util.genai`, `opentelemetry.instrumentation.genai`, or
  `strands.telemetry`; normalizes `chat`/`invoke_agent`/`execute_tool` spans
  into the canonical vocabulary and strips content-bearing attributes and
  events. Claude Code groups keep priority, so a group is never emitted twice.
```

Keep the configuration table unchanged (the new edge has no settings) and
note under the example that `coding_agent/claude` also handles the GenAI
sources; the instance name is historical.

- [ ] **Step 3: Update docs/design.md**

Make these changes:

1. In "Component shape", change the traces edge description to "traces to
   traces: stateless native-trace normalization (Claude Code and GenAI
   semconv sources) behind a claiming router".
2. Add a new section after "Claude Code normalization" titled
   `## GenAI semconv normalization` containing, condensed from the spec:
   the three sources and their two semconv modes, the scope-prefix table,
   the claiming rules (Claude first, whole resource group copied, unclaimed
   groups stay raw-only), the name/attribute normalization rules including
   legacy-token mapping and provenance attributes, the content-stripping
   lists, and the decision record (stateless, no root synthesis; strip
   content in canonical; Strands provider names the framework).
3. In "Research basis", add the 2026-08-19 research summary and the four
   primary-source links from the spec.
4. In "Known limitations and future work", replace the line starting "Only
   Codex log synthesis and Claude Code native-span normalization" with a
   list that includes the GenAI edge, and add: opt-in root synthesis for
   rootless traces, configurable scope allowlist, and the upstream
   package-rename risk.
5. In "Testing strategy", add one paragraph each for the openai-adhoc E2E
   (two semconv modes in one paid run, direct chat-completions against z.ai,
   no responses-proxy) and the Strands E2E (native tree in raw, normalized
   tree in canonical, content present in raw and absent in canonical).

Copy exact wording from
`docs/superpowers/specs/2026-08-20-genai-semconv-traces-design.md` where it
fits; that document is the source of truth for these rules.

- [ ] **Step 4: Verify everything still builds and passes**

```bash
go test ./... && (cd connector/codingagentconnector && go test -race ./...)
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add README.md connector/codingagentconnector/README.md docs/design.md
git commit -m "docs: document GenAI semconv trace sources"
```

---

## Verification checklist (run after the last task)

```bash
go test ./... && (cd connector/codingagentconnector && go test -race ./...)
go test -tags=e2e ./e2e/validator/
shellcheck scripts/e2e-openai.sh scripts/e2e-strands.sh e2e/openai-adhoc/run.sh e2e/strands/run.sh
E2E_RUN_ID=v OPENAI_API_KEY=v docker compose -f compose.e2e-openai.yaml config --quiet
E2E_RUN_ID=v OPENAI_API_KEY=v docker compose -f compose.e2e-strands.yaml config --quiet
docker build --tag openai-adhoc-e2e:dev e2e/openai-adhoc
docker build --tag strands-e2e:dev e2e/strands
```

The paid live runs (`scripts/e2e-openai.sh`, `scripts/e2e-strands.sh` with a
real `OPENAI_API_KEY`) stay manual and are the final acceptance step the user
triggers when ready.
