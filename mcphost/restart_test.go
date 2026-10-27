package mcphost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jrniemiec/keel/runner"
)

// TestMain runs the test binary as an MCP server when KEEL_TEST_SERVER=1,
// so tests can start a real server process that they can make die.
func TestMain(m *testing.M) {
	if os.Getenv("KEEL_TEST_SERVER") == "1" {
		runTestServer()
		return
	}
	os.Exit(m.Run())
}

// runTestServer serves tools over stdio: echo returns its text, die exits
// the process in the middle of the call, name returns KEEL_TEST_NAME (which
// server answered), read is marked read-only and idempotent, fail returns
// a result marked isError, and bad.name has a name the model APIs reject.
func runTestServer() {
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	text := func(t string) *mcp.CallToolResult {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: t}}}
	}
	mcp.AddTool(s, &mcp.Tool{Name: "name"}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		return text(os.Getenv("KEEL_TEST_NAME")), nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "read", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		return text("read"), nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "fail"}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		res := text("no such file")
		res.IsError = true
		return res, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "bad.name"}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		return text("bad"), nil, nil
	})
	type echoArgs struct {
		Text string `json:"text"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "echo"}, func(ctx context.Context, req *mcp.CallToolRequest, args echoArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text}}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "die"}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		fmt.Fprintln(os.Stderr, "dying")
		os.Exit(3)
		return nil, nil, nil
	})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func TestCallRestartsDeadServerOnce(t *testing.T) {
	ctx := context.Background()
	h, err := Start(ctx, []Server{{Name: "t", Command: os.Args[0], Env: []string{"KEEL_TEST_SERVER=1"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	echo := runner.ToolCall{ID: "c", Name: "t__echo", Args: []byte(`{"text":"hi"}`)}
	die := runner.ToolCall{ID: "c", Name: "t__die", Args: []byte(`{}`)}

	if out, _, err := h.Call(ctx, echo); err != nil || out != "hi" {
		t.Fatalf("echo = %q, %v", out, err)
	}
	if _, _, err := h.Call(ctx, die); !errors.Is(err, runner.ErrServerDown) {
		t.Fatalf("die: err = %v, want ErrServerDown", err)
	}
	if out, _, err := h.Call(ctx, echo); err != nil || out != "hi" {
		t.Fatalf("echo after restart = %q, %v", out, err)
	}
	if _, _, err := h.Call(ctx, die); !errors.Is(err, runner.ErrServerDown) {
		t.Fatalf("second die: err = %v, want ErrServerDown", err)
	}
	_, _, err = h.Call(ctx, echo)
	if !errors.Is(err, runner.ErrServerDown) || !strings.Contains(err.Error(), "restarted once already") {
		t.Fatalf("echo after second death: err = %v, want restarted once already", err)
	}
}
