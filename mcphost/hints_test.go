package mcphost

import (
	"slices"
	"testing"

	"github.com/jrniemiec/keel/runner"
)

func TestWithOverrides(t *testing.T) {
	yes, no := true, false
	overrides := []HintOverride{
		{Tools: "read_*", ReadOnly: &yes, Destructive: &no},
		{Tools: "read_secret", ReadOnly: &no},   // later wins
		{Tools: "write_file", Idempotent: &yes}, // other hints kept
		{Tools: "nothing_*", ReadOnly: &yes},    // matches no tool
	}
	server := runner.Hints{Destructive: true, OpenWorld: true} // mcp-go's defaults
	tests := []struct {
		tool string
		want runner.Hints
	}{
		{"read_file", runner.Hints{ReadOnly: true, OpenWorld: true}},
		{"read_secret", runner.Hints{OpenWorld: true}},
		{"write_file", runner.Hints{Destructive: true, Idempotent: true, OpenWorld: true}},
		{"delete_file", server},
	}
	used := make([]bool, len(overrides))
	for _, tt := range tests {
		if got := withOverrides(tt.tool, server, overrides, used); got != tt.want {
			t.Errorf("%s: hints = %+v, want %+v", tt.tool, got, tt.want)
		}
	}
	if want := []bool{true, true, true, false}; !slices.Equal(used, want) {
		t.Errorf("used = %v, want %v", used, want)
	}
}
