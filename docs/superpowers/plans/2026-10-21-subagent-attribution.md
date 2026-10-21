# Subagent Attribution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to carry out this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Carry which agent produced a span and which agent spawned it into canonical output under `gen_ai.agent.id` and a new `coding_agent.agent.parent_id`, across Claude Code, Codex, and OpenHands, and map Claude Code's `organization.id` to `coding_agent.team.id`.

**Architecture:** One new vocabulary key. Each edge copies its wire's agent identity verbatim where the wire carries it: Claude Code on any span type (`parent_agent_id` beside the `agent_id` mapping PR #109 landed), Codex on `execute_tool` spans from `agent_name`, OpenHands on a delegate group's root from the `…metadata.subagent_type` and `…metadata.parent_session_id` association properties the root already inherits. No new spans, no reparenting.

**Tech Stack:** Go, OpenTelemetry Collector pdata (`ptrace`/`pcommon`), testify.

**Spec:** `docs/superpowers/specs/2026-10-19-harness-feature-gaps-design.md`, sections 3 and 6. Read it first.

## Global Constraints

- Work from `connector/codingagentconnector/` for Go commands; prefix every `go` command with `GOTOOLCHAIN=go1.27.0`.
- `GOTOOLCHAIN=go1.27.0 ./scripts/check.sh` from the repo root must end `ALL CHECKS PASSED` before pushing.
- Both agent keys copy **verbatim** and are **absent when the wire omits them**; `capture_identity` does not control them (an agent id is not personal data).
- `coding_agent.team.id` from Claude Code's `organization.id` **does** follow `capture_identity`, like every other identity key.
- Codex's main agent keeps `/root` as its `gen_ai.agent.id`; the connector never rewrites it.
- Fixture values stay synthetic. Stage explicit paths only.
- PR #115 (OpenHands tool call id and max_tokens) touches `internal/openhands/normalizer.go`; rebase onto `main` after it merges before starting Task 4.

## Review Focus

1. **A non-delegate OpenHands conversation must carry neither agent key.** Task 4 tests a plain conversation root.
2. **An OpenHands fragment without its conversation root must still inherit the delegate keys from children.** The synthetic root reads association properties from kept children; Task 4 tests a root-less delegate group.
3. **Claude's `parent_agent_id` must map on every span type that carries it, not only the root.** Task 2 tests it on an `llm_request` span.
4. **Claude's `organization.id` must stay off output when `capture_identity` is off.** Task 2 tests both settings.
5. **A Codex `tool_result` without `agent_name` (pre-0.163.0 wires) must produce a tool span with no agent key.** Task 3 tests it.

---

### Task 1: Vocabulary gains `coding_agent.agent.parent_id`

**Files:**
- Change: `connector/codingagentconnector/internal/canonical/vocabulary.go:59`
- Change: `docs/canonical-attributes.md`

- [ ] **Step 1: Add the key and watch the mirror test fail**

In `vocabulary.go`, append `"coding_agent.agent.parent_id",` after `"coding_agent.terminal.type",` (the last entry; keep `coding_agent.usage.cost` where plan 1 put it if that plan landed first).

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/canonical/ -run TestVocabularyDocsMirror -v`
Expected: FAIL, printing the block the doc must contain.

- [ ] **Step 2: Update the doc**

In `docs/canonical-attributes.md`, inside the generated block, append `- `coding_agent.agent.parent_id`` as the last line. After the `coding_agent.request.service_tier` paragraph (and the cost paragraph if present), add:

```markdown
`gen_ai.agent.id` names the agent that produced a span, verbatim from the wire: Claude Code's `agent_id` (a subagent's id; the main agent omits it), Codex's `agent_name` on tool results (`/root` for the main agent, a subagent's path, nickname, or conversation id otherwise), and OpenHands' `…metadata.subagent_type` on a delegate's root. `coding_agent.agent.parent_id` names the agent that spawned it, where the wire says so: Claude Code's `parent_agent_id` and OpenHands' `…metadata.parent_session_id`. Both are absent on the main agent's spans and whenever the wire omits them; neither is identity, so `capture_identity` does not control them. The connector synthesizes no per-subagent spans: the two keys join a subagent's existing chat and tool spans to the agent that spawned it.
```

In the "Connector namespace" provenance bullet, add `coding_agent.agent.parent_id` after `coding_agent.terminal.type`.

- [ ] **Step 3: Run the mirror test**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/canonical/ -count=1`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add connector/codingagentconnector/internal/canonical/vocabulary.go docs/canonical-attributes.md
git commit -m "feat(canonical): add coding_agent.agent.parent_id"
```

### Task 2: Claude Code maps `parent_agent_id` and `organization.id`

**Files:**
- Change: `connector/codingagentconnector/internal/claude/normalizer.go:100-136`
- Test: `connector/codingagentconnector/internal/claude/normalizer_test.go`
- Change: `connector/codingagentconnector/internal/claude/conformance_test.go`
- Change: `connector/codingagentconnector/internal/claude/testdata/claude-native-traces.json`
- Change: `connector/codingagentconnector/internal/claude/testdata/claude-canonical.otlp.json`
- Change: `connector/codingagentconnector/conformance_test.go` (claude fixture signals)
- Change: `docs/harnesses/claude-code.md`

- [ ] **Step 1: Write the failing tests**

Append to `connector/codingagentconnector/internal/claude/normalizer_test.go` (it has `traceSink`, `reassemble`, `findSpan`, `attrString`):

```go
// TestClaudeMapsParentAgentID pins coding_agent.agent.parent_id: verbatim on
// any span type that carries parent_agent_id, absent otherwise, and never
// gated by captureIdentity.
func TestClaudeMapsParentAgentID(t *testing.T) {
	buildInput := func() ptrace.Traces {
		input := ptrace.NewTraces()
		rs := input.ResourceSpans().AppendEmpty()
		rs.Resource().Attributes().PutStr("service.name", "claude-code")
		spans := rs.ScopeSpans().AppendEmpty().Spans()
		llm := spans.AppendEmpty()
		llm.SetName("claude_code.llm_request")
		llm.Attributes().PutStr("agent_id", "agent-child")
		llm.Attributes().PutStr("parent_agent_id", "agent-main")
		tool := spans.AppendEmpty()
		tool.SetName("claude_code.tool")
		tool.Attributes().PutStr("parent_agent_id", "agent-main")
		spans.AppendEmpty().SetName("claude_code.interaction")
		return input
	}
	for _, captureIdentity := range []bool{true, false} {
		sink := &traceSink{}
		require.NoError(t, New(sink, captureIdentity).ConsumeTraces(context.Background(), buildInput()))
		all := reassemble(sink)
		chat := findSpan(t, all, "chat")
		require.Equal(t, "agent-child", attrString(t, chat, "gen_ai.agent.id"))
		require.Equal(t, "agent-main", attrString(t, chat, "coding_agent.agent.parent_id"))
		require.Equal(t, "agent-main", attrString(t, findSpan(t, all, "execute_tool"), "coding_agent.agent.parent_id"))
		root := findSpan(t, all, "invoke_agent claude_code")
		_, ok := root.Attributes().Get("coding_agent.agent.parent_id")
		require.False(t, ok, "no parent_agent_id on the wire, no key")
		_, ok = chat.Attributes().Get("parent_agent_id")
		require.False(t, ok, "the raw key must not survive")
	}
}

// TestClaudeMapsOrganizationToTeam pins coding_agent.team.id from
// organization.id on the interaction root, behind captureIdentity.
func TestClaudeMapsOrganizationToTeam(t *testing.T) {
	buildInput := func() ptrace.Traces {
		input := ptrace.NewTraces()
		rs := input.ResourceSpans().AppendEmpty()
		rs.Resource().Attributes().PutStr("service.name", "claude-code")
		spans := rs.ScopeSpans().AppendEmpty().Spans()
		root := spans.AppendEmpty()
		root.SetName("claude_code.interaction")
		root.Attributes().PutStr("organization.id", "org-00000000-0000-0000-0000-000000000000")
		spans.AppendEmpty().SetName("claude_code.llm_request")
		return input
	}
	onSink := &traceSink{}
	require.NoError(t, New(onSink, true).ConsumeTraces(context.Background(), buildInput()))
	onRoot := findSpan(t, reassemble(onSink), "invoke_agent claude_code")
	require.Equal(t, "org-00000000-0000-0000-0000-000000000000", attrString(t, onRoot, "coding_agent.team.id"))
	_, ok := onRoot.Attributes().Get("organization.id")
	require.False(t, ok, "the raw key must not survive")

	offSink := &traceSink{}
	require.NoError(t, New(offSink, false).ConsumeTraces(context.Background(), buildInput()))
	offRoot := findSpan(t, reassemble(offSink), "invoke_agent claude_code")
	_, ok = offRoot.Attributes().Get("coding_agent.team.id")
	require.False(t, ok, "coding_agent.team.id must be gated behind captureIdentity")

	bareSink := &traceSink{}
	bare := ptrace.NewTraces()
	bare.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty().SetName("claude_code.interaction")
	require.NoError(t, New(bareSink, true).ConsumeTraces(context.Background(), bare))
	_, ok = findSpan(t, reassemble(bareSink), "invoke_agent claude_code").Attributes().Get("coding_agent.team.id")
	require.False(t, ok, "no organization.id on the wire, no key")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/claude/ -run 'ParentAgentID|OrganizationToTeam' -v`
Expected: both FAIL on the first `attrString` of the new key.

- [ ] **Step 3: Write the code**

In `normalizeClaudeSpan`, change the opening block to:

```go
	// agent_id is the subagent's own id and parent_agent_id the agent that
	// spawned it, so canonical output names them gen_ai.agent.id and
	// coding_agent.agent.parent_id verbatim on any span type that carries
	// them. user.email is PII, so it follows user.id behind captureIdentity.
	if agentID := firstSpanString(span, "agent_id"); agentID != "" {
		span.Attributes().PutStr("gen_ai.agent.id", agentID)
	}
	if parentID := firstSpanString(span, "parent_agent_id"); parentID != "" {
		span.Attributes().PutStr("coding_agent.agent.parent_id", parentID)
	}
```

In the `claude_code.interaction` case, extend the `captureIdentity` block:

```go
		if captureIdentity {
			if uid := firstSpanString(span, "user.id"); uid != "" {
				span.Attributes().PutStr("coding_agent.user.id", uid)
			}
			// organization.id is the Anthropic organization, the
			// billing-scale group; Cursor's team id is the same scale.
			if org := firstSpanString(span, "organization.id"); org != "" {
				span.Attributes().PutStr("coding_agent.team.id", org)
			}
		}
```

- [ ] **Step 4: Run the package tests**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/claude/ -count=1`
Expected: PASS.

- [ ] **Step 5: Extend the fixtures with a synthetic organization id**

The 2.1.304 capture carries no `organization.id`. Add one to the native fixture's interaction span and the matching `coding_agent.team.id` to the canonical fixture's root. Both files are JSONL (one OTLP export per line). Run from the repo root:

```bash
python3 -I - <<'EOF'
import json
native = 'connector/codingagentconnector/internal/claude/testdata/claude-native-traces.json'
canon = 'connector/codingagentconnector/internal/claude/testdata/claude-canonical.otlp.json'
ORG = 'org-00000000-0000-0000-0000-000000000000'

def rewrite(path, span_name, key, value):
    out = []
    hits = 0
    for line in open(path):
        if not line.strip():
            out.append(line); continue
        d = json.loads(line)
        for rs in d['resourceSpans']:
            for ss in rs['scopeSpans']:
                for s in ss['spans']:
                    if s['name'] == span_name and not any(a['key'] == key for a in s['attributes']):
                        s['attributes'].append({'key': key, 'value': {'stringValue': value}})
                        hits += 1
        out.append(json.dumps(d, separators=(',', ':')) + '\n')
    open(path, 'w').writelines(out)
    print(path, 'spans updated:', hits)

rewrite(native, 'claude_code.interaction', 'organization.id', ORG)
rewrite(canon, 'invoke_agent claude_code', 'coding_agent.team.id', ORG)
EOF
```

Expected: one span updated in each file. If `git diff --stat` shows the whole file reformatted (the original used different separators), re-run with `separators=(', ', ': ')` to match, so the diff stays to the two attributes.

- [ ] **Step 6: Add the conformance signal and run everything**

In `connector/codingagentconnector/internal/claude/conformance_test.go` and in the `claude` fixture of `connector/codingagentconnector/conformance_test.go`, append:

```go
{RawKey: "organization.id", CanonicalKey: "coding_agent.team.id", Kind: canonical.Presence},
```

Run from `connector/codingagentconnector/`: `GOTOOLCHAIN=go1.27.0 go test ./... -count=1`
Expected: PASS.

Run from the repo root: `GOTOOLCHAIN=go1.27.0 go test ./e2e/validator/ -run Claude -count=1`
Expected: PASS (the validator checks the canonical fixture against the vocabulary; `coding_agent.team.id` is canonical).

- [ ] **Step 7: Update the matrix**

In `docs/harnesses/claude-code.md`:

- In the `claude_code.interaction` table, replace the `organization.id, user.account_uuid, user.account_id` row with:

```
| `organization.id` | interaction | `coding_agent.team.id` | mapped (only when `capture_identity` is on; absent when the span carries no `organization.id` — the 2.1.304 capture carries none, and the committed fixture synthesizes one; the Anthropic organization is the billing-scale group, the same scale as Cursor's team id) |
| `user.account_uuid`, `user.account_id` | all span types | — | dropped (standard attributes, documented upstream; absent in the 2.1.304 capture) |
```

- Add after the `agent_id` row:

```
| `parent_agent_id` | all span types | `coding_agent.agent.parent_id` | mapped (verbatim; absent when the span carries none — Claude Code sets it only on a subagent's spans — and absent in the 2.1.304 capture; `capture_identity` does not control it) |
```

- In the `llm_request` table, change the `query_source, parent_agent_id, workflow.run_id, workflow.name` row to drop `parent_agent_id` from the list; in the `tool` table, change `result_tokens, parent_agent_id, workflow.run_id, workflow.name` likewise.
- In "Canonical keys with no Claude Code source", delete the `coding_agent.team.id` row and change the `coding_agent.user.email / coding_agent.team.id` row (if still present) to mention only what remains unmapped.

- [ ] **Step 8: Commit**

```bash
git add connector/codingagentconnector/internal/claude/normalizer.go connector/codingagentconnector/internal/claude/normalizer_test.go connector/codingagentconnector/internal/claude/conformance_test.go connector/codingagentconnector/internal/claude/testdata/claude-native-traces.json connector/codingagentconnector/internal/claude/testdata/claude-canonical.otlp.json connector/codingagentconnector/conformance_test.go docs/harnesses/claude-code.md
git commit -m "feat(claude): map parent agent id and organization id"
```

### Task 3: Codex maps `agent_name` onto tool spans

**Files:**
- Change: `connector/codingagentconnector/internal/codex/trace.go:197-228` (`appendToolSpans`)
- Test: `connector/codingagentconnector/internal/codex/trace_test.go`
- Change: `connector/codingagentconnector/internal/codex/conformance_test.go`
- Change: `connector/codingagentconnector/internal/codex/testdata/codex-canonical.otlp.json`
- Change: `connector/codingagentconnector/conformance_test.go` (codex fixture signals)
- Change: `docs/harnesses/codex.md:147`

- [ ] **Step 1: Write the failing test**

Append to `connector/codingagentconnector/internal/codex/trace_test.go` (it has `testEvent`, `mustBuildTrace`, `findSpan`, `attrString`):

```go
// TestToolSpanCarriesAgentName pins gen_ai.agent.id on execute_tool spans:
// Codex 0.163.0 stamps agent_name on every tool_result ("/root" for the main
// agent, a subagent's path otherwise), copied verbatim; an older wire without
// it yields a tool span with no agent key.
func TestToolSpanCarriesAgentName(t *testing.T) {
	base := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	turn := &turnState{
		conversationID: "conversation-1", first: base, last: base.Add(3 * time.Second), promptSeen: true,
		resource: map[string]any{"service.name": "codex_cli_rs"},
		events: []agentEvent{
			testEvent("codex.user_prompt", base, nil),
			testEvent("codex.tool_result", base.Add(time.Second), map[string]any{"tool_name": "shell", "call_id": "call-1", "agent_name": "/root"}),
			testEvent("codex.tool_result", base.Add(2*time.Second), map[string]any{"tool_name": "read_file", "call_id": "call-2", "agent_name": "/root/explorer"}),
			testEvent("codex.tool_result", base.Add(3*time.Second), map[string]any{"tool_name": "apply_patch", "call_id": "call-3"}),
		},
	}
	spans := mustBuildTrace(t, turn, "completed").ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	require.Equal(t, "/root", attrString(t, findSpan(t, spans, "execute_tool shell"), "gen_ai.agent.id"))
	require.Equal(t, "/root/explorer", attrString(t, findSpan(t, spans, "execute_tool read_file"), "gen_ai.agent.id"))
	_, ok := findSpan(t, spans, "execute_tool apply_patch").Attributes().Get("gen_ai.agent.id")
	require.False(t, ok, "no agent_name on the wire, no key")
	_, ok = findSpan(t, spans, "execute_tool shell").Attributes().Get("agent_name")
	require.False(t, ok, "the raw key must not survive")
	_, ok = findSpan(t, spans, "invoke_agent codex").Attributes().Get("gen_ai.agent.id")
	require.False(t, ok, "the root names the harness via gen_ai.agent.name, not a subagent")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/codex/ -run TestToolSpanCarriesAgentName -v`
Expected: FAIL on the first `attrString`.

- [ ] **Step 3: Write the code**

In `appendToolSpans`, after `span.Attributes().PutStr("coding_agent.source.event", event.name)`, add:

```go
		// agent_name is the agent that made the call: "/root" for the main
		// agent, a subagent's path, nickname, or conversation id otherwise.
		// It copies verbatim; the connector never renames the main agent.
		if agent := stringValue(event.attrs["agent_name"]); agent != "" {
			span.Attributes().PutStr("gen_ai.agent.id", agent)
		}
```

- [ ] **Step 4: Run the package tests**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/codex/ -count=1`
Expected: PASS.

- [ ] **Step 5: Update the canonical fixture and add the conformance signal**

The committed native fixture already carries `agent_name: "/root"` on both `codex.tool_result` records (0.163.0 capture). The canonical fixture came from the live run, so add the key by hand to its two `execute_tool` spans. From the repo root:

```bash
python3 -I - <<'EOF'
import json
path = 'connector/codingagentconnector/internal/codex/testdata/codex-canonical.otlp.json'
out, hits = [], 0
for line in open(path):
    if not line.strip():
        out.append(line); continue
    d = json.loads(line)
    for rs in d['resourceSpans']:
        for ss in rs['scopeSpans']:
            for s in ss['spans']:
                if s['name'].startswith('execute_tool') and not any(a['key'] == 'gen_ai.agent.id' for a in s['attributes']):
                    s['attributes'].append({'key': 'gen_ai.agent.id', 'value': {'stringValue': '/root'}})
                    hits += 1
    out.append(json.dumps(d, separators=(',', ':')) + '\n')
open(path, 'w').writelines(out)
print('spans updated:', hits)
EOF
```

Expected: `spans updated: 2`. As in Task 2, match the file's original separators if the diff reformats it.

In `connector/codingagentconnector/internal/codex/conformance_test.go` and in the `codex` fixture of `connector/codingagentconnector/conformance_test.go`, append:

```go
{RawKey: "agent_name", CanonicalKey: "gen_ai.agent.id", Kind: canonical.Presence},
```

Run from `connector/codingagentconnector/`: `GOTOOLCHAIN=go1.27.0 go test ./... -count=1`, then from the repo root `GOTOOLCHAIN=go1.27.0 go test ./e2e/validator/ -run Codex -count=1`.
Expected: PASS.

- [ ] **Step 6: Update the matrix**

In `docs/harnesses/codex.md`, replace the `agent_name` row (line 147) with:

```
| `agent_name` | `gen_ai.agent.id` | mapped (verbatim on execute_tool spans; `/root` for the main agent, as in the pinned capture, otherwise the subagent's path, nickname, or conversation UUID; absent on wires older than 0.163.0 and on a record without it; the root keeps `gen_ai.agent.name` = `codex` and no agent id) |
```

In "Canonical keys with no Codex source", change the `gen_ai.agent.id / gen_ai.agent.version` row to `gen_ai.agent.version` only, and add `| `coding_agent.agent.parent_id` | not provided (Codex names the calling agent but not its parent) |`.

- [ ] **Step 7: Commit**

```bash
git add connector/codingagentconnector/internal/codex/trace.go connector/codingagentconnector/internal/codex/trace_test.go connector/codingagentconnector/internal/codex/conformance_test.go connector/codingagentconnector/internal/codex/testdata/codex-canonical.otlp.json connector/codingagentconnector/conformance_test.go docs/harnesses/codex.md
git commit -m "feat(codex): map agent_name onto tool spans as gen_ai.agent.id"
```

### Task 4: OpenHands delegate keys on the root

Rebase onto `main` first if PR #115 has merged (`git fetch origin && git rebase origin/main`).

**Files:**
- Change: `connector/codingagentconnector/internal/openhands/normalizer.go:394-427` (`putRootAttributes`; constants near `attrIsDelegate`)
- Test: `connector/codingagentconnector/internal/openhands/normalizer_test.go` (replace `TestDelegateLinkageDropped`)
- Change: `docs/harnesses/openhands.md:138-160`

- [ ] **Step 1: Replace the failing-by-design test with the new contract**

In `normalizer_test.go`, replace `TestDelegateLinkageDropped` with:

```go
// TestDelegateRootCarriesAgentKeys pins subagent attribution: a delegate
// group's root names the subagent (gen_ai.agent.id ← subagent_type) and the
// conversation that spawned it (coding_agent.agent.parent_id ←
// parent_session_id); task ids and the severed parent trace/span ids still
// drop, and a non-delegate root carries neither key.
func TestDelegateRootCarriesAgentKeys(t *testing.T) {
	attrs := baseAttrs()
	attrs[attrIsDelegate] = "true"
	attrs[attrMetadata+"task_id"] = "task-9"
	attrs[attrMetadata+"subagent_type"] = "bash_delegate"
	attrs[attrMetadata+"parent_session_id"] = "parent-conversation-uuid"
	attrs[attrMetadata+"delegate.parent_trace_id"] = "0123"
	delegated := makeTraces(makeSpan(traceB, spanSpec{
		name: "conversation", spanID: "cccccccccccccccc", attrs: attrs,
	}))
	root := findByName(t, normalizeOne(t, delegated), "invoke_agent openhands")[0]
	require.Equal(t, "bash_delegate", attrString(root, "gen_ai.agent.id"))
	require.Equal(t, "parent-conversation-uuid", attrString(root, "coding_agent.agent.parent_id"))
	require.Equal(t, "openhands", attrString(root, "gen_ai.agent.name"), "the harness name is unchanged")
	root.Attributes().Range(func(k string, _ pcommon.Value) bool {
		require.NotContains(t, k, "coding_agent.openhands")
		require.NotContains(t, k, "lmnr.")
		return true
	})

	plain := findByName(t, normalizeOne(t, makeTraces(makeSpan(traceA, conversationSpec()))), "invoke_agent openhands")[0]
	_, ok := plain.Attributes().Get("gen_ai.agent.id")
	require.False(t, ok, "a non-delegate conversation names no subagent")
	_, ok = plain.Attributes().Get("coding_agent.agent.parent_id")
	require.False(t, ok)
}

// TestDelegateFragmentInheritsAgentKeys pins that a delegate batch exported
// without its conversation root still carries the keys on its synthetic root,
// read from the kept children the way session_id is.
func TestDelegateFragmentInheritsAgentKeys(t *testing.T) {
	attrs := baseAttrs()
	attrs[attrIsDelegate] = "true"
	attrs[attrMetadata+"subagent_type"] = "bash_delegate"
	attrs[attrMetadata+"parent_session_id"] = "parent-conversation-uuid"
	attrs["gen_ai.request.model"] = "glm-5.3-flash"
	fragment := makeTraces(makeSpan(traceB, spanSpec{
		name: "litellm.completion", spanID: "eeeeeeeeeeeeeeee", spanType: "LLM", attrs: attrs,
	}))
	root := findByName(t, normalizeOne(t, fragment), "invoke_agent openhands")[0]
	require.Equal(t, "bash_delegate", attrString(root, "gen_ai.agent.id"))
	require.Equal(t, "parent-conversation-uuid", attrString(root, "coding_agent.agent.parent_id"))
}
```

`conversationSpec()` already exists in the test file (used by `TestOutputOrderStableUnderShuffledInput`); it builds a plain `conversation` root with `baseAttrs()`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/openhands/ -run 'DelegateRoot|DelegateFragment' -v`
Expected: both FAIL on the first `require.Equal` (`attrString` returns `""`).

- [ ] **Step 3: Write the code**

Add constants after `attrIsDelegate`:

```go
	attrSubagentType    = attrMetadata + "subagent_type"
	attrParentSessionID = attrMetadata + "parent_session_id"
```

In `putRootAttributes`, after the `gen_ai.conversation.id` block and before the `captureIdentity` block, add:

```go
	// A delegate's root names the subagent and the conversation that
	// spawned it. Neither is identity, so the flag does not control them;
	// a non-delegate conversation carries neither key on the wire.
	if subagent := firstString(src, attrSubagentType); subagent != "" {
		attrs.PutStr("gen_ai.agent.id", subagent)
	}
	if parent := firstString(src, attrParentSessionID); parent != "" {
		attrs.PutStr("coding_agent.agent.parent_id", parent)
	}
```

The `src` map already inherits every `lmnr.association.properties.` key from kept children when the root is synthetic, so the fragment test passes without further change.

- [ ] **Step 4: Run the package tests**

Run: `GOTOOLCHAIN=go1.27.0 go test ./internal/openhands/ -count=1`
Expected: PASS. The committed fixture carries no delegate metadata, so the canonical fixture needs no change.

- [ ] **Step 5: Update the matrix**

In `docs/harnesses/openhands.md`, replace the `…metadata.subagent_type` and `…metadata.parent_session_id` rows with:

```
| `…metadata.subagent_type` | `gen_ai.agent.id` | mapped (verbatim, on the delegate's invoke_agent root; absent on a non-delegate conversation and in the 2026-10-19 capture, which ran no delegate; a root-less fragment inherits it from kept children) |
| `…metadata.parent_session_id` | `coding_agent.agent.parent_id` | mapped (same conditions; the conversation id of the agent that spawned the delegate) |
```

Rewrite the "Delegate linkage note" paragraph to:

```markdown
**Delegate linkage note.** `…metadata.is_delegate = true` claims a group for
this edge, `…metadata.tool_call_id` drives the call/result dedupe internally,
and the delegate's root carries the subagent type as `gen_ai.agent.id` and
the spawning conversation as `coding_agent.agent.parent_id`, so a delegate's
spans join to their parent conversation on that key. The task id and the
severed parent's trace and span ids stay out of canonical output and are
recoverable only through a raw-preservation branch. The SDK writes these keys
in [`_delegate_observability_metadata`](...) and [`detached_delegate_context`](...)
(keep the two existing links; v1.56.0, checked 2026-10-19).
```

In "Canonical keys with no OpenHands source", change the `gen_ai.agent.id / gen_ai.agent.version` row to `gen_ai.agent.version` only.

- [ ] **Step 6: Commit**

```bash
git add connector/codingagentconnector/internal/openhands/normalizer.go connector/codingagentconnector/internal/openhands/normalizer_test.go docs/harnesses/openhands.md
git commit -m "feat(openhands): name the subagent and parent conversation on delegate roots"
```

### Task 5: Cross-harness docs and the other matrices

**Files:**
- Change: `docs/harnesses/opencode.md`, `docs/harnesses/cursor.md`, `docs/harnesses/pi.md`, `docs/harnesses/genai-scopes.md` ("no source" tables)

- [ ] **Step 1: Add `coding_agent.agent.parent_id` as not provided**

Add to each "Canonical keys with no … source" table:

OpenCode: `| `coding_agent.agent.parent_id` | not provided (the AI SDK spans name no agent) |`
Cursor: `| `coding_agent.agent.parent_id` | not provided (subagents get their own `cursor.conversation.id`; outside Grok Bot the wire has no parent rollup, and `cursor.grok_bot.root_turn.id` is a turn id, not an agent id) |`
Pi: `| `coding_agent.agent.parent_id` | not provided |`
GenAI scopes (the "no GenAI-edge source" table): `| `coding_agent.agent.parent_id` | all four | not provided (Copilot's `gen_ai.agent.id` names the agent; no emitter names a parent) |`

- [ ] **Step 2: Commit**

```bash
git add docs/harnesses/opencode.md docs/harnesses/cursor.md docs/harnesses/pi.md docs/harnesses/genai-scopes.md
git commit -m "docs: record which harnesses carry no parent agent id"
```

### Task 6: Full check and live stacks

- [ ] **Step 1: Run the repo check**

Run from the repo root: `GOTOOLCHAIN=go1.27.0 ./scripts/check.sh`
Expected: ends `ALL CHECKS PASSED`.

- [ ] **Step 2: Run the three affected live stacks**

```bash
GOTOOLCHAIN=go1.27.0 ./scripts/e2e-claude.sh
GOTOOLCHAIN=go1.27.0 ./scripts/e2e.sh
GOTOOLCHAIN=go1.27.0 ./scripts/e2e-openhands.sh
```

Expected: each validator run passes. After the Codex run, `rg -c '"gen_ai.agent.id"' .e2e-output/canonical-traces.json` reports at least 1 (the `/root` agent on the tool span). Claude and OpenHands runs exercise no subagent, so their output gains nothing; the runs confirm nothing regressed.

- [ ] **Step 3: Open the PR**

Push the branch and open a PR titled `feat: subagent attribution and Claude organization id` with the spec path and live-stack results in the body.
