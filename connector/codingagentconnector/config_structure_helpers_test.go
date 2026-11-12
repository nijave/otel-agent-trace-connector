// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package codingagentconnector

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeGo(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o600))
	return dir
}

func TestHarnessReferences(t *testing.T) {
	const claude = `"` + internalPrefix + `claude"`
	const canonical = `"` + internalPrefix + `canonical"`
	tests := []struct {
		name string
		src  string
		want map[string]map[string]bool
	}{
		{
			name: "claimer only",
			src:  "package p\nimport (\n" + claude + "\n" + canonical + "\n)\nvar _ = claude.Claimer\nvar _ = canonical.X\n",
			want: map[string]map[string]bool{"claude": {"Claimer": true}},
		},
		{
			name: "other identifier",
			src:  "package p\nimport " + claude + "\nvar _ = claude.ContainsClaudeSpans\n",
			want: map[string]map[string]bool{"claude": {"ContainsClaudeSpans": true}},
		},
		{
			name: "aliased import",
			src:  "package p\nimport c " + claude + "\nvar _ = c.Helper\n",
			want: map[string]map[string]bool{"claude": {"Helper": true}},
		},
		{
			name: "dot import",
			src:  "package p\nimport . " + claude + "\nvar _ = Claimer\n",
			want: map[string]map[string]bool{"claude": {".": true}},
		},
		{
			name: "blank import",
			src:  "package p\nimport _ " + claude + "\n",
			want: map[string]map[string]bool{"claude": {"_": true}},
		},
		{
			name: "import with no reference",
			src:  "package p\nimport " + claude + "\n",
			want: map[string]map[string]bool{"claude": {}},
		},
		{
			name: "shadowing local is not a reference",
			src:  "package p\nimport " + claude + "\nvar _ = claude.Claimer\nfunc f() { claude := struct{ X int }{}; _ = claude.X }\n",
			want: map[string]map[string]bool{"claude": {"Claimer": true}},
		},
		{
			name: "shared only",
			src:  "package p\nimport " + canonical + "\nvar _ = canonical.X\n",
			want: map[string]map[string]bool{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := harnessReferences(writeGo(t, tt.src))
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDeclaresClaimer(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want bool
	}{
		{"typed var", "package p\nvar Claimer canonical.Claimer = claimer{}\n", true},
		{"typed var in group", "package p\nvar (\n\tx = 1\n\tClaimer canonical.Claimer = claimer{}\n)\n", true},
		{"untyped var", "package p\nvar Claimer = claimer{}\n", false},
		{"wrong type", "package p\nvar Claimer other.Claimer = claimer{}\n", false},
		{"func", "package p\nfunc Claimer() {}\n", false},
		{"absent", "package p\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := declaresClaimer(writeGo(t, tt.src))
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
