package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/collector/pdata/ptrace"
)

func main() {
	runID := os.Getenv("E2E_RUN_ID")
	path := os.Getenv("TRACE_FILE")
	agent := os.Getenv("E2E_AGENT")
	if agent == "" {
		agent = "codex"
	}
	if runID == "" || path == "" {
		fail("E2E_RUN_ID and TRACE_FILE are required")
	}
	if agent != "codex" && agent != "claude_code" {
		fail(fmt.Sprintf("unsupported E2E_AGENT %q", agent))
	}
	rawPath := os.Getenv("RAW_TRACE_FILE")
	if agent == "claude_code" && rawPath == "" {
		fail("RAW_TRACE_FILE is required for Claude Code validation")
	}
	deadline := time.Now().Add(45 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := validateCanonicalFile(path, runID, agent); err != nil {
			lastErr = err
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if agent == "claude_code" {
			if err := validateClaudeRawFile(rawPath, runID); err != nil {
				lastErr = err
				time.Sleep(250 * time.Millisecond)
				continue
			}
		}
		if agent == "claude_code" {
			fmt.Printf("validated raw and canonical Claude Code traces for run %s\n", runID)
		} else {
			fmt.Printf("validated canonical Codex trace for run %s\n", runID)
		}
		return
	}
	fail(fmt.Sprintf("E2E traces did not become valid: %v", lastErr))
}

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
	for scanner.Scan() {
		traces, err := unmarshaler.UnmarshalTraces(scanner.Bytes())
		if err != nil {
			continue
		}
		if err := validate(traces, runID); err == nil {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("no matching complete trace found")
}

func validateCanonicalTraces(traces ptrace.Traces, runID, agent string) error {
	rootName := "invoke_agent " + agent
	for i := 0; i < traces.ResourceSpans().Len(); i++ {
		rs := traces.ResourceSpans().At(i)
		value, ok := rs.Resource().Attributes().Get("e2e.run.id")
		if !ok || value.Str() != runID {
			continue
		}
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			if agent == "claude_code" {
				if err := rejectClaudeTraceContent(spans); err != nil {
					return err
				}
			}
			for k := 0; k < spans.Len(); k++ {
				root := spans.At(k)
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
				for childIndex := 0; childIndex < spans.Len(); childIndex++ {
					child := spans.At(childIndex)
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
		}
	}
	return errors.New("run id was not found")
}

func validateClaudeRawTraces(traces ptrace.Traces, runID string) error {
	for i := 0; i < traces.ResourceSpans().Len(); i++ {
		rs := traces.ResourceSpans().At(i)
		value, ok := rs.Resource().Attributes().Get("e2e.run.id")
		if !ok || value.Str() != runID {
			continue
		}
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			if err := rejectClaudeTraceContent(spans); err != nil {
				return err
			}
			for k := 0; k < spans.Len(); k++ {
				root := spans.At(k)
				if root.Name() != "claude_code.interaction" {
					continue
				}
				if root.ParentSpanID() != [8]byte{} {
					return errors.New("raw Claude root unexpectedly has a parent")
				}
				llm, tool := false, false
				for childIndex := 0; childIndex < spans.Len(); childIndex++ {
					child := spans.At(childIndex)
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
		}
	}
	return errors.New("claude run id was not found")
}

func rejectClaudeTraceContent(spans ptrace.SpanSlice) error {
	for i := 0; i < spans.Len(); i++ {
		span := spans.At(i)
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
func fail(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }
