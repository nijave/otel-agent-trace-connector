// Package validator holds the OTLP trace assertions for the live e2e tests. The
// pure checks here are unit-tested in validator_test.go; the live path that reads
// real collector output is in live_test.go (behind the `e2e` build tag).
package validator

import (
	"bufio"
	"errors"
	"fmt"
	"os"

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
	// The collector's batch processor can split one logical trace across several
	// OTLP batches (each written as its own line), so merge every batch before
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

func validateCanonicalTraces(traces ptrace.Traces, runID, agent string) error {
	rootName := "invoke_agent " + agent
	spans := collectRunSpans(traces, runID)
	if len(spans) == 0 {
		return errors.New("run id was not found")
	}
	if agent == "claude_code" {
		if err := rejectClaudeTraceContent(spans); err != nil {
			return err
		}
	}
	for _, root := range spans {
		if root.Name() != rootName {
			continue
		}
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
		for _, sensitive := range []string{"prompt", "arguments", "output"} {
			if _, exists := root.Attributes().Get(sensitive); exists {
				return fmt.Errorf("sensitive root attribute %q was copied", sensitive)
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
					return errors.New("claude chat model is missing")
				}
			case "execute_tool":
				tool = true
				if agent == "claude_code" && stringAttr(child, "gen_ai.tool.name") != "Bash" {
					return errors.New("claude Bash tool span is missing")
				}
			}
			for _, sensitive := range []string{"prompt", "arguments", "output"} {
				if _, exists := child.Attributes().Get(sensitive); exists {
					return fmt.Errorf("sensitive attribute %q was copied", sensitive)
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
	}
	return errors.New("root span was not found")
}

func validateClaudeRawTraces(traces ptrace.Traces, runID string) error {
	spans := collectRunSpans(traces, runID)
	if len(spans) == 0 {
		return errors.New("claude run id was not found")
	}
	if err := rejectClaudeTraceContent(spans); err != nil {
		return err
	}
	for _, root := range spans {
		if root.Name() != "claude_code.interaction" {
			continue
		}
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
	}
	return errors.New("raw Claude interaction root was not found")
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
func boolAttr(span ptrace.Span, key string) bool {
	value, ok := span.Attributes().Get(key)
	return ok && value.Bool()
}
