// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package canonical

import (
	"fmt"
	"strconv"
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// SignalKind selects how a signal compares raw input to canonical output.
type SignalKind int

const (
	// Sum requires the canonical key's total across output to equal the raw
	// key's total across native input; catches partial or missing mapping of
	// token counters.
	Sum SignalKind = iota
	// Presence requires the canonical key to appear somewhere in output
	// whenever the raw key appears anywhere in input.
	Presence
)

// Signal declares one source-backed pairing of a native attribute with its
// canonical counterpart.
type Signal struct {
	RawKey       string     // native fixture attribute/log field
	CanonicalKey string     // required counterpart in output
	Kind         SignalKind // sum | presence
}

// RawInput is the un-normalized telemetry an edge consumed.
type RawInput struct {
	Traces ptrace.Traces
	Logs   plog.Logs
}

// Edge describes one harness pipeline: how to load its raw fixture and run it
// through the full normalization path, plus the signals the wire guarantees.
type Edge struct {
	Name      string
	LoadRaw   func() (RawInput, error)
	Normalize func(RawInput) (ptrace.Traces, error)
	Signals   []Signal
}

// Check runs the three-tier contract over an edge and returns one entry per
// violation: required keys present, only canonical attributes emitted, and
// declared signals accounted for.
func Check(e Edge) []string {
	var raw RawInput
	if e.LoadRaw != nil {
		loaded, err := e.LoadRaw()
		if err != nil {
			return []string{fmt.Sprintf("harness %s: load raw: %v", e.Name, err)}
		}
		raw = loaded
	}
	if raw.Traces == (ptrace.Traces{}) {
		raw.Traces = ptrace.NewTraces()
	}
	if raw.Logs == (plog.Logs{}) {
		raw.Logs = plog.NewLogs()
	}
	out, err := e.Normalize(raw)
	if err != nil {
		return []string{fmt.Sprintf("harness %s: normalize: %v", e.Name, err)}
	}
	var errs []string
	errs = append(errs, checkRequired(e.Name, out)...)
	errs = append(errs, checkAllowed(e.Name, out)...)
	errs = append(errs, checkResource(e.Name, out)...)
	errs = append(errs, checkSignals(e.Name, raw, out, e.Signals)...)
	return errs
}

// Conformance runs Check and fails the test per violation.
func Conformance(t *testing.T, e Edge) {
	t.Helper()
	for _, err := range Check(e) {
		t.Errorf("%s", err)
	}
}

func checkRequired(name string, out ptrace.Traces) []string {
	var errs []string
	for _, span := range spans(out) {
		attrs := span.Attributes()
		for _, key := range requiredKeys {
			if _, ok := attrs.Get(key); !ok {
				errs = append(errs, fmt.Sprintf("harness %s: span %q missing required %s", name, span.Name(), key))
			}
		}
	}
	return errs
}

func checkAllowed(name string, out ptrace.Traces) []string {
	seen := map[string]bool{}
	var errs []string
	report := func(attrs pcommon.Map) {
		attrs.Range(func(key string, _ pcommon.Value) bool {
			if seen[key] || IsCanonicalAttribute(key) {
				return true
			}
			seen[key] = true
			errs = append(errs, fmt.Sprintf("harness %s: forbidden attribute %s on canonical span", name, key))
			return true
		})
	}
	for _, span := range spans(out) {
		report(span.Attributes())
		events := span.Events()
		for i := 0; i < events.Len(); i++ {
			report(events.At(i).Attributes())
		}
	}
	return errs
}

// checkResource validates output resource attributes against the canonical
// resource vocabulary and requires service.name on every resource group.
func checkResource(name string, out ptrace.Traces) []string {
	var errs []string
	rss := out.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		attrs := rss.At(i).Resource().Attributes()
		for _, key := range requiredResourceKeys {
			if _, ok := attrs.Get(key); !ok {
				errs = append(errs, fmt.Sprintf("harness %s: resource missing required %s", name, key))
			}
		}
		attrs.Range(func(key string, _ pcommon.Value) bool {
			if !IsCanonicalResourceKey(key) {
				errs = append(errs, fmt.Sprintf("harness %s: forbidden resource attribute %s", name, key))
			}
			return true
		})
	}
	return errs
}

func checkSignals(name string, raw RawInput, out ptrace.Traces, signals []Signal) []string {
	rawTotals := map[string]int64{}
	rawPresent := map[string]bool{}
	collectRaw := func(attrs pcommon.Map) {
		attrs.Range(func(key string, value pcommon.Value) bool {
			rawPresent[key] = true
			if n, ok := numericValue(value); ok {
				rawTotals[key] += n
			}
			return true
		})
	}
	rss := raw.Traces.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		sss := rss.At(i).ScopeSpans()
		for j := 0; j < sss.Len(); j++ {
			ss := sss.At(j).Spans()
			for k := 0; k < ss.Len(); k++ {
				collectRaw(ss.At(k).Attributes())
			}
		}
	}
	rls := raw.Logs.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		sls := rls.At(i).ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			records := sls.At(j).LogRecords()
			for k := 0; k < records.Len(); k++ {
				collectRaw(records.At(k).Attributes())
			}
		}
	}

	outTotals := map[string]int64{}
	outCount := map[string]bool{}
	for _, span := range spans(out) {
		span.Attributes().Range(func(key string, value pcommon.Value) bool {
			if n, ok := numericValue(value); ok {
				outTotals[key] += n
			}
			outCount[key] = true
			return true
		})
	}

	var errs []string
	for _, sig := range signals {
		if !rawPresent[sig.RawKey] {
			continue
		}
		switch sig.Kind {
		case Sum:
			if got := outTotals[sig.CanonicalKey]; got != rawTotals[sig.RawKey] {
				errs = append(errs, fmt.Sprintf("harness %s: signal %s sum mismatch for %s: raw total %d, output total %d",
					name, sig.RawKey, sig.CanonicalKey, rawTotals[sig.RawKey], got))
			}
		case Presence:
			if !outCount[sig.CanonicalKey] {
				errs = append(errs, fmt.Sprintf("harness %s: signal %s present in raw but %s absent from output",
					name, sig.RawKey, sig.CanonicalKey))
			}
		}
	}
	return errs
}

func spans(traces ptrace.Traces) []ptrace.Span {
	var result []ptrace.Span
	rss := traces.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		sss := rss.At(i).ScopeSpans()
		for j := 0; j < sss.Len(); j++ {
			ss := sss.At(j).Spans()
			for k := 0; k < ss.Len(); k++ {
				result = append(result, ss.At(k))
			}
		}
	}
	return result
}

// numericValue coerces numeric-looking attribute values to int64 following
// cursor.Int64Value semantics: int/int32/int64 pass through, doubles
// truncate, uint64 is guarded against overflow, strings parse as integers.
func numericValue(v pcommon.Value) (int64, bool) {
	switch v.Type() {
	case pcommon.ValueTypeInt:
		return v.Int(), true
	case pcommon.ValueTypeDouble:
		return int64(v.Double()), true
	case pcommon.ValueTypeStr:
		parsed, err := strconv.ParseInt(v.Str(), 10, 64)
		return parsed, err == nil
	}
	return 0, false
}
