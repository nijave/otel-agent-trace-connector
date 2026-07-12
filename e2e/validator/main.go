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
	if runID == "" || path == "" {
		fail("E2E_RUN_ID and TRACE_FILE are required")
	}
	deadline := time.Now().Add(45 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := validateFile(path, runID); err == nil {
			fmt.Printf("validated canonical Codex trace for run %s\n", runID)
			return
		} else {
			lastErr = err
		}
		time.Sleep(250 * time.Millisecond)
	}
	fail(fmt.Sprintf("canonical trace did not become valid: %v", lastErr))
}

func validateFile(path, runID string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	unmarshaler := &ptrace.JSONUnmarshaler{}
	for scanner.Scan() {
		traces, err := unmarshaler.UnmarshalTraces(scanner.Bytes())
		if err != nil {
			continue
		}
		if err := validateTraces(traces, runID); err == nil {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("no matching complete trace found")
}

func validateTraces(traces ptrace.Traces, runID string) error {
	for i := 0; i < traces.ResourceSpans().Len(); i++ {
		rs := traces.ResourceSpans().At(i)
		value, ok := rs.Resource().Attributes().Get("e2e.run.id")
		if !ok || value.Str() != runID {
			continue
		}
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				root := spans.At(k)
				if root.Name() != "invoke_agent codex" {
					continue
				}
				if root.ParentSpanID() != [8]byte{} {
					return errors.New("root span unexpectedly has a parent")
				}
				if !boolAttr(root, "coding_agent.turn.complete") {
					return errors.New("root turn is incomplete")
				}
				if stringAttr(root, "gen_ai.operation.name") != "invoke_agent" {
					return errors.New("root operation is not invoke_agent")
				}
				if stringAttr(root, "gen_ai.conversation.id") == "" {
					return errors.New("conversation id is missing")
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
					case "execute_tool":
						tool = true
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
