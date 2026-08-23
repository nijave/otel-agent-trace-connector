// Package validator holds the OTLP trace assertions for the live e2e tests. The
// pure checks here are unit-tested in validator_test.go; the live path that reads
// real collector output is in live_test.go (behind the `e2e` build tag).
package validator

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/collector/pdata/ptrace"
)

func validateCanonicalFile(path, runID, agent string) error {
	return validateTraceFile(path, runID, func(traces ptrace.Traces, runID string) error {
		return validateCanonicalTraces(traces, runID, agent)
	})
}

func validateClaudeRawFile(path, runID string) error {
	return validateTraceFile(path, runID, validateClaudeRawTraces)
}

func validateTraceFile(path, runID string, validate func(ptrace.Traces, string) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	unmarshaler := &ptrace.JSONUnmarshaler{}
	// One logical trace arrives as several OTLP exports, each written as its own
	// line: agents flush spans as they end, so the interaction root lands in a
	// later export than the children it parents. Merge every batch before
	// validating rather than expecting a complete trace within a single line.
	merged := ptrace.NewTraces()
	for scanner.Scan() {
		traces, err := unmarshaler.UnmarshalTraces(scanner.Bytes())
		if err != nil {
			continue
		}
		traces.ResourceSpans().MoveAndAppendTo(merged.ResourceSpans())
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return validate(merged, runID)
}

// collectRunSpans flattens every span whose resource carries the matching
// e2e.run.id across all batches. Reassembling the trace this way (like a real
// trace backend) decouples validation from how the batch processor happened to
// group spans.
func collectRunSpans(traces ptrace.Traces, runID string) []ptrace.Span {
	var spans []ptrace.Span
	for i := 0; i < traces.ResourceSpans().Len(); i++ {
		rs := traces.ResourceSpans().At(i)
		value, ok := rs.Resource().Attributes().Get("e2e.run.id")
		if !ok || value.Str() != runID {
			continue
		}
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			ss := rs.ScopeSpans().At(j).Spans()
			for k := 0; k < ss.Len(); k++ {
				spans = append(spans, ss.At(k))
			}
		}
	}
	return spans
}

// firstValidRoot walks every span named rootName and returns nil as soon as one
// satisfies validateRoot. A run legitimately contains more than one candidate: the
// Codex connector emits a root per turn, and a turn finalized by inactivity
// timeout, eviction or supersession is incomplete by design, so hard-failing on
// whichever candidate happens to come first would reject a run that did produce a
// good trace. When none validates, the last candidate's error is the useful
// diagnostic; notFound covers the case where there were no candidates at all.
func firstValidRoot(spans []ptrace.Span, rootName string, notFound string, validateRoot func(ptrace.Span) error) error {
	var lastErr error
	for _, root := range spans {
		if root.Name() != rootName {
			continue
		}
		if err := validateRoot(root); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New(notFound)
}

func validateCanonicalTraces(traces ptrace.Traces, runID, agent string) error {
	spans := collectRunSpans(traces, runID)
	if len(spans) == 0 {
		return errors.New("run id was not found")
	}
	// Checked across every span in the run rather than only the root and its direct
	// children, so a leak on a deeper span cannot slip through.
	if err := rejectSensitiveAttrs(spans); err != nil {
		return err
	}
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
	case "opencode":
		if err := rejectGenAIContent(spans); err != nil {
			return err
		}
		if err := rejectOpenCodeContent(spans); err != nil {
			return err
		}
	case "openhands":
		return validateOpenHandsCanonicalTraces(traces, runID)
	case "pi":
		if err := rejectGenAIContent(spans); err != nil {
			return err
		}
	case "copilot":
		if err := rejectGenAIContent(spans); err != nil {
			return err
		}
		return validateCopilotSpans(spans)
	}
	if agent == "claude_code" {
		if err := rejectClaudeTraceContent(spans); err != nil {
			return err
		}
	}
	return firstValidRoot(spans, "invoke_agent "+agent, "root span was not found", func(root ptrace.Span) error {
		if root.ParentSpanID() != [8]byte{} {
			return errors.New("root span unexpectedly has a parent")
		}
		if agent == "codex" && !boolAttr(root, "coding_agent.turn.complete") {
			return errors.New("root turn is incomplete")
		}
		if stringAttr(root, "gen_ai.operation.name") != "invoke_agent" {
			return errors.New("root operation is not invoke_agent")
		}
		if stringAttr(root, "gen_ai.conversation.id") == "" {
			return errors.New("conversation id is missing")
		}
		if agent == "opencode" {
			if _, ok := root.Attributes().Get("gen_ai.usage.input_tokens"); !ok {
				return errors.New("opencode root usage is missing")
			}
			if stringAttr(root, "coding_agent.client.name") != "opencode" {
				return errors.New("opencode client name is missing")
			}
		}
		if agent == "claude_code" {
			if stringAttr(root, "gen_ai.provider.name") != "anthropic" {
				return errors.New("claude provider is not anthropic")
			}
			if stringAttr(root, "coding_agent.client.name") != "claude_code" {
				return errors.New("claude client name is missing")
			}
			if stringAttr(root, "telemetry.source") != "native" {
				return errors.New("claude telemetry source is not native")
			}
		}
		chat, tool := false, false
		for _, child := range spans {
			if child.ParentSpanID() != root.SpanID() || child.TraceID() != root.TraceID() {
				continue
			}
			switch stringAttr(child, "gen_ai.operation.name") {
			case "chat":
				chat = true
				if agent == "codex" {
					if _, ok := child.Attributes().Get("gen_ai.usage.input_tokens"); !ok {
						return errors.New("chat input token usage is missing")
					}
				} else if stringAttr(child, "gen_ai.request.model") == "" {
					return errors.New("chat child span is missing its request model")
				}
			case "execute_tool":
				tool = true
				if agent == "claude_code" && stringAttr(child, "gen_ai.tool.name") != "Bash" {
					return errors.New("claude Bash tool span is missing")
				}
				if agent == "opencode" && stringAttr(child, "gen_ai.tool.name") != "bash" {
					return errors.New("opencode bash tool span is missing")
				}
			}
		}
		if !chat {
			return errors.New("chat child span is missing")
		}
		if !tool {
			return errors.New("execute_tool child span is missing")
		}
		return nil
	})
}

func validateClaudeRawTraces(traces ptrace.Traces, runID string) error {
	spans := collectRunSpans(traces, runID)
	if len(spans) == 0 {
		return errors.New("claude run id was not found")
	}
	if err := rejectClaudeTraceContent(spans); err != nil {
		return err
	}
	return firstValidRoot(spans, "claude_code.interaction", "raw Claude interaction root was not found", func(root ptrace.Span) error {
		if root.ParentSpanID() != [8]byte{} {
			return errors.New("raw Claude root unexpectedly has a parent")
		}
		llm, tool := false, false
		for _, child := range spans {
			if child.ParentSpanID() != root.SpanID() || child.TraceID() != root.TraceID() {
				continue
			}
			switch child.Name() {
			case "claude_code.llm_request":
				llm = true
				if stringAttr(child, "model") == "" {
					return errors.New("raw Claude model is missing")
				}
			case "claude_code.tool":
				tool = true
				if stringAttr(child, "tool_name") != "Bash" {
					return errors.New("raw Claude Bash tool span is missing")
				}
			}
		}
		if !llm || !tool {
			return errors.New("raw Claude LLM or tool child is missing")
		}
		return nil
	})
}

// rejectSensitiveAttrs fails if any span carries vendor content that normalization
// must never copy onto a canonical span.
func rejectSensitiveAttrs(spans []ptrace.Span) error {
	for _, span := range spans {
		for _, key := range []string{"prompt", "arguments", "output"} {
			if _, exists := span.Attributes().Get(key); exists {
				return fmt.Errorf("sensitive attribute %q was copied to span %q", key, span.Name())
			}
		}
	}
	return nil
}

func rejectClaudeTraceContent(spans []ptrace.Span) error {
	for _, span := range spans {
		if err := rejectClaudeContent(span); err != nil {
			return err
		}
		for eventIndex := 0; eventIndex < span.Events().Len(); eventIndex++ {
			event := span.Events().At(eventIndex)
			switch event.Name() {
			case "tool.output", "api_request_body", "api_response_body":
				return fmt.Errorf("sensitive Claude span event %q was captured", event.Name())
			}
		}
	}
	return nil
}

func rejectClaudeContent(span ptrace.Span) error {
	for _, key := range []string{"tool_input", "full_command", "response.model_output"} {
		if _, ok := span.Attributes().Get(key); ok {
			return fmt.Errorf("sensitive Claude attribute %q was captured", key)
		}
	}
	if value, ok := span.Attributes().Get("user_prompt"); ok && value.Str() != "<REDACTED>" {
		return errors.New("claude user prompt was not redacted")
	}
	return nil
}

func stringAttr(span ptrace.Span, key string) string {
	value, ok := span.Attributes().Get(key)
	if !ok {
		return ""
	}
	return value.Str()
}

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
				// A failed model attempt (for example a retried rate limit)
				// ends its chat span with no usage; only a successful call
				// proves the canonical usage mapping, so skip bare attempts.
				if _, ok := span.Attributes().Get("gen_ai.usage.input_tokens"); !ok {
					continue
				}
				if stringAttr(span, "gen_ai.request.model") == "" {
					return errors.New("strands chat model is missing")
				}
				chat = true
			case "execute_tool":
				if stringAttr(span, "gen_ai.tool.name") == "get_marker" {
					tool = true
				}
			}
		}
		if !chat {
			return errors.New("strands chat span with usage is missing")
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

// validateCopilotSpans requires one valid invoke_agent root. Copilot roots
// carry a producer-chosen subject (BYOK providers rename gen_ai.agent.name),
// so candidates match the operation prefix rather than an exact name, and the
// first valid candidate wins like every other agent path.
func validateCopilotSpans(spans []ptrace.Span) error {
	var lastErr error
	for _, root := range spans {
		if !strings.HasPrefix(root.Name(), "invoke_agent") {
			continue
		}
		if err := validateCopilotTree(spans, root); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("copilot root span was not found")
}

func validateCopilotTree(spans []ptrace.Span, root ptrace.Span) error {
	if root.ParentSpanID() != [8]byte{} {
		return errors.New("root span unexpectedly has a parent")
	}
	if stringAttr(root, "gen_ai.operation.name") != "invoke_agent" {
		return errors.New("root operation is not invoke_agent")
	}
	if stringAttr(root, "gen_ai.conversation.id") == "" {
		return errors.New("conversation id is missing")
	}
	if _, ok := root.Attributes().Get("gen_ai.usage.input_tokens"); !ok {
		return errors.New("copilot root input usage is missing")
	}
	if _, ok := root.Attributes().Get("gen_ai.usage.output_tokens"); !ok {
		return errors.New("copilot root output usage is missing")
	}
	if stringAttr(root, "telemetry.source") != "native" {
		return errors.New("telemetry source is not native")
	}
	if stringAttr(root, "coding_agent.client.name") == "" {
		return errors.New("client name is missing")
	}
	chat, tool := false, false
	for _, child := range spans {
		if child.ParentSpanID() != root.SpanID() || child.TraceID() != root.TraceID() {
			continue
		}
		switch stringAttr(child, "gen_ai.operation.name") {
		case "chat":
			chat = true
			if stringAttr(child, "gen_ai.request.model") == "" {
				return errors.New("chat child span is missing its request model")
			}
		case "execute_tool":
			tool = true
		}
	}
	if !chat {
		return errors.New("chat child span is missing")
	}
	if !tool {
		return errors.New("execute_tool child span is missing")
	}
	return nil
}

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

func boolAttr(span ptrace.Span, key string) bool {
	value, ok := span.Attributes().Get(key)
	return ok && value.Bool()
}

// allSpans flattens every span across all resource and scope groups, like
// collectRunSpans without the run-id filter: fixture files carry no run id.
func allSpans(traces ptrace.Traces) []ptrace.Span {
	var spans []ptrace.Span
	for i := 0; i < traces.ResourceSpans().Len(); i++ {
		ss := traces.ResourceSpans().At(i).ScopeSpans()
		for j := 0; j < ss.Len(); j++ {
			group := ss.At(j).Spans()
			for k := 0; k < group.Len(); k++ {
				spans = append(spans, group.At(k))
			}
		}
	}
	return spans
}

// validateCursorCanonicalFile asserts the canonical Cursor shape over the
// connector's committed fixture, keeping the wire's no-content guarantee under
// test without an Enterprise Cursor tenant.
func validateCursorCanonicalFile(path string) error {
	return validateTraceFile(path, "", validateCursorCanonicalTraces)
}

func validateCursorCanonicalTraces(traces ptrace.Traces, _ string) error {
	spans := allSpans(traces)
	if err := validateCursorSpans(spans); err != nil {
		return err
	}
	return rejectSensitiveAttrs(spans)
}

func validateCursorSpans(spans []ptrace.Span) error {
	var roots, chats int
	for _, span := range spans {
		switch {
		case strings.HasPrefix(span.Name(), "invoke_agent cursor"):
			roots++
			if err := validateCursorRoot(span); err != nil {
				return err
			}
		case span.Name() == "chat" || strings.HasPrefix(span.Name(), "chat "):
			chats++
			if err := validateCursorChat(span); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected span %q in cursor canonical output", span.Name())
		}
	}
	if roots == 0 {
		return errors.New("no invoke_agent cursor root found")
	}
	if chats == 0 {
		return errors.New("no chat spans found under cursor root")
	}
	return nil
}

func validateCursorRoot(span ptrace.Span) error {
	if got := stringAttr(span, "gen_ai.conversation.id"); got == "" {
		return errors.New("cursor root missing gen_ai.conversation.id")
	}
	if got := stringAttr(span, "coding_agent.turn.finish_reason"); got == "" {
		return errors.New("cursor root missing finish reason")
	}
	if _, ok := span.Attributes().Get("gen_ai.provider.name"); ok {
		return errors.New("cursor root must not claim gen_ai.provider.name")
	}
	if _, ok := span.Attributes().Get("coding_agent.turn.complete"); ok {
		return errors.New("cursor root must not claim completion")
	}
	return nil
}

func validateCursorChat(span ptrace.Span) error {
	if got := stringAttr(span, "gen_ai.operation.name"); got != "chat" {
		return fmt.Errorf("cursor chat span operation %q", got)
	}
	if span.StartTimestamp() != span.EndTimestamp() {
		return errors.New("cursor chat span must stay a point span; the wire carries no durations")
	}
	return nil
}

// validateOpenHandsCanonicalFile asserts the canonical OpenHands shape over
// a committed fixture.
func validateOpenHandsCanonicalFile(path string) error {
	return validateTraceFile(path, "", validateOpenHandsCanonicalTraces)
}

// validateOpenHandsRawFile pins the raw wire shape the normalizer claims:
// marker spans and LLM spans present under the lmnr.tracer scope.
func validateOpenHandsRawFile(path, _ string) error {
	return validateTraceFile(path, "", validateOpenHandsRawTraces)
}

func validateOpenHandsCanonicalTraces(traces ptrace.Traces, _ string) error {
	spans := allSpans(traces)
	if err := validateOpenHandsSpans(spans); err != nil {
		return err
	}
	// The raw lmnr.tracer wire carries gen_ai content attributes, so the
	// canonical check needs the genai strip contract, not just the vendor trio.
	if err := rejectGenAIContent(spans); err != nil {
		return err
	}
	return rejectSensitiveAttrs(spans)
}

func validateOpenHandsRawTraces(traces ptrace.Traces, _ string) error {
	spans := allSpans(traces)
	var markers, llm int
	for _, span := range spans {
		switch span.Name() {
		case "conversation", "agent.step", "agent.astep":
			markers++
		case "litellm.completion", "litellm.responses":
			llm++
		}
	}
	if markers == 0 {
		return fmt.Errorf("no openhands marker spans in raw capture")
	}
	if llm == 0 {
		return fmt.Errorf("no llm spans in raw openhands capture")
	}
	return nil
}

func validateOpenHandsSpans(spans []ptrace.Span) error {
	var roots, others int
	for _, span := range spans {
		switch {
		case span.Name() == "invoke_agent openhands":
			roots++
			if got := stringAttr(span, "gen_ai.conversation.id"); got == "" {
				return fmt.Errorf("openhands root missing gen_ai.conversation.id")
			}
			if got := stringAttr(span, "gen_ai.agent.name"); got != "openhands" {
				return fmt.Errorf("openhands root agent name %q", got)
			}
		case strings.HasPrefix(span.Name(), "chat"):
			others++
			if got := stringAttr(span, "gen_ai.operation.name"); got != "chat" {
				return fmt.Errorf("openhands chat span operation %q", got)
			}
		case strings.HasPrefix(span.Name(), "execute_tool"):
			others++
			if got := stringAttr(span, "gen_ai.operation.name"); got != "execute_tool" {
				return fmt.Errorf("openhands tool span operation %q", got)
			}
		default:
			return fmt.Errorf("unexpected span %q in openhands canonical output", span.Name())
		}
	}
	if roots == 0 {
		return fmt.Errorf("no invoke_agent openhands root found")
	}
	if others == 0 {
		return fmt.Errorf("no chat or execute_tool children found under openhands root")
	}
	return nil
}
