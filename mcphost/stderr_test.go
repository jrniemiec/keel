package mcphost

import (
	"bytes"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jrniemiec/keel/internal/klog"
)

func TestStderrLog(t *testing.T) {
	long := strings.Repeat("x", maxStderrLine+10)
	tests := []struct {
		name   string
		writes []string
		want   []string // logged lines, after flush
	}{
		{"lines in one write", []string{"one\ntwo\n"}, []string{"one", "two"}},
		{"line across writes", []string{"start", "ing up\n"}, []string{"starting up"}},
		{"crlf", []string{"one\r\n"}, []string{"one"}},
		{"empty lines skipped", []string{"\n\none\n"}, []string{"one"}},
		{"last line on flush", []string{"one\npanic: boom"}, []string{"one", "panic: boom"}},
		{"long line cut", []string{long + "\n"}, []string{long[:maxStderrLine] + "…"}},
		{"long line without newline", []string{long}, []string{long[:maxStderrLine] + "…"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logged bytes.Buffer
			w := &stderrLog{server: "fs", log: klog.New(&logged, klog.LevelInfo)}
			for _, p := range tt.writes {
				if n, err := w.Write([]byte(p)); n != len(p) || err != nil {
					t.Fatalf("Write = %d, %v", n, err)
				}
			}
			w.flush()
			if got := loggedLines(t, logged.String()); !slices.Equal(got, tt.want) {
				t.Errorf("logged %q, want %q", got, tt.want)
			}
		})
	}
}

var stderrEntry = regexp.MustCompile(`msg="mcp stderr" server=fs line=(.*)`)

// loggedLines returns the line attribute of each mcp stderr entry in out.
func loggedLines(t *testing.T, out string) []string {
	t.Helper()
	var lines []string
	for _, m := range stderrEntry.FindAllStringSubmatch(out, -1) {
		line := m[1]
		if strings.HasPrefix(line, `"`) { // slog quotes values with spaces
			line = strings.Trim(line, `"`)
		}
		lines = append(lines, line)
	}
	return lines
}
