package mcphost

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jrniemiec/keel/runner"
)

// startTestServers starts the test binary as an MCP server once per name;
// its name tool returns the server's name.
func startTestServers(t *testing.T, names ...string) *Host {
	t.Helper()
	var servers []Server
	for _, n := range names {
		servers = append(servers, Server{Name: n, Command: os.Args[0], Env: []string{"KEEL_TEST_SERVER=1", "KEEL_TEST_NAME=" + n}})
	}
	h, err := Start(context.Background(), servers)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

func TestCallRoutesByPrefix(t *testing.T) {
	h := startTestServers(t, "a", "b")
	tests := []struct {
		tool, want string
		isError    bool
	}{
		{"a__name", "a", false},
		{"b__name", "b", false},
		{"c__name", "unknown tool c__name", true},
		{"name", "unknown tool name", true},
	}
	for _, tt := range tests {
		out, isError, err := h.Call(context.Background(), runner.ToolCall{ID: "c", Name: tt.tool, Args: []byte(`{}`)})
		if err != nil || out != tt.want || isError != tt.isError {
			t.Errorf("Call(%s) = %q, %v, %v; want %q, %v, nil", tt.tool, out, isError, err, tt.want, tt.isError)
		}
	}
}

func TestList(t *testing.T) {
	h := startTestServers(t, "b", "a")
	tools, err := h.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	byName := map[string]runner.ToolDef{}
	for _, d := range tools {
		names = append(names, d.Name)
		byName[d.Name] = d
	}

	// Servers in name order; bad.name left out.
	want := []string{"a__die", "a__echo", "a__fail", "a__name", "a__read", "b__die", "b__echo", "b__fail", "b__name", "b__read"}
	if len(names) != len(want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("tools = %v, want %v", names, want)
		}
	}

	// echo has no annotations: the MCP defaults. read sets read-only and
	// idempotent; destructive and open world stay at their defaults.
	defaults := runner.Hints{Destructive: true, OpenWorld: true}
	read := runner.Hints{ReadOnly: true, Idempotent: true, Destructive: true, OpenWorld: true}
	if got := byName["a__echo"].Hints; got != defaults {
		t.Errorf("a__echo hints = %+v, want %+v", got, defaults)
	}
	if got := byName["a__read"].Hints; got != read {
		t.Errorf("a__read hints = %+v, want %+v", got, read)
	}
}

// A tool's failure goes to the model as isError; the server stays up.
func TestCallToolFailure(t *testing.T) {
	h := startTestServers(t, "a")
	ctx := context.Background()
	tests := []struct{ tool, want string }{
		{"a__fail", "no such file"}, // the tool's isError
		{"a__nope", "nope"},         // JSON-RPC error: unknown tool
	}
	for _, tt := range tests {
		out, isError, err := h.Call(ctx, runner.ToolCall{ID: "c", Name: tt.tool, Args: []byte(`{}`)})
		if err != nil || !isError || !strings.Contains(out, tt.want) {
			t.Errorf("Call(%s) = %q, %v, %v; want text with %q, isError, no error", tt.tool, out, isError, err, tt.want)
		}
	}
	if h.down["a"] {
		t.Error("server marked down after tool failures")
	}
	if out, _, err := h.Call(ctx, runner.ToolCall{ID: "c", Name: "a__name", Args: []byte(`{}`)}); err != nil || out != "a" {
		t.Errorf("Call(a__name) after failures = %q, %v", out, err)
	}
}

func TestText(t *testing.T) {
	tests := []struct {
		name string
		res  *mcp.CallToolResult
		want string
	}{
		{"text joined", &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: "one"}, &mcp.TextContent{Text: "two"},
		}}, "one\ntwo"},
		{"placeholders", &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.ImageContent{MIMEType: "image/png", Data: make([]byte, 120)},
			&mcp.AudioContent{MIMEType: "audio/wav", Data: make([]byte, 7)},
			&mcp.ResourceLink{URI: "file:///a.txt"},
			&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///b.bin"}},
		}}, "[image image/png, 120 bytes]\n[audio audio/wav, 7 bytes]\n[resource link file:///a.txt]\n[resource file:///b.bin]"},
		{"embedded text", &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///c.txt", Text: "inside"}},
		}}, "inside"},
		{"structured without text", &mcp.CallToolResult{StructuredContent: map[string]any{"n": 3}}, `{"n":3}`},
		{"structured ignored with text", &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: "3"}},
			StructuredContent: map[string]any{"n": 3},
		}, "3"},
		{"long text kept whole", &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", 100_000)}}}, strings.Repeat("x", 100_000)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := text(tt.res); got != tt.want {
				t.Errorf("text = %q, want %q", got, tt.want)
			}
		})
	}
}
