// Package mcphost runs MCP servers as child processes over stdio and offers
// their tools to the runner: Host implements runner.Tools. A tool is named
// <server>__<tool>, e.g. "fs__read_file".
package mcphost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jrniemiec/keel/internal/klog"
	"github.com/jrniemiec/keel/runner"
)

// Sep joins a server's name and its tool's name.
const Sep = "__"

// MaxOut caps the text of one tool result, so a large file read can't fill
// the model's context.
const MaxOut = 64 << 10

// validName is what Anthropic and OpenAI accept as a tool name.
var validName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// Server is an MCP server started as a child process.
type Server struct {
	Name    string   // prefixes its tools; letters, digits, '-' or '_', no "__"
	Command string   // e.g. "mcp-filesystem-server"
	Args    []string // e.g. the directories it may access
	Env     []string // "KEY=value", added to keel's environment
}

// Host holds a session with each started server.
type Host struct {
	sessions map[string]*mcp.ClientSession // by server name
}

// Start starts the servers and connects to them. On error, the servers
// already started are closed.
func Start(ctx context.Context, servers []Server) (*Host, error) {
	h := &Host{sessions: map[string]*mcp.ClientSession{}}
	client := mcp.NewClient(&mcp.Implementation{Name: "keel", Version: "dev"}, nil)
	for _, s := range servers {
		if !validName.MatchString(s.Name) || strings.Contains(s.Name, Sep) {
			h.Close()
			return nil, fmt.Errorf("mcp server name %q: want letters, digits, '-' or '_', no %q", s.Name, Sep)
		}
		if _, dup := h.sessions[s.Name]; dup {
			h.Close()
			return nil, fmt.Errorf("mcp server %q listed twice", s.Name)
		}
		cmd := exec.Command(s.Command, s.Args...)
		cmd.Env = append(os.Environ(), s.Env...)
		cs, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
		if err != nil {
			h.Close()
			return nil, fmt.Errorf("start mcp server %s: %w", s.Name, err)
		}
		h.sessions[s.Name] = cs
		info := cs.InitializeResult().ServerInfo
		klog.Info(klog.MsgMCPUp, "server", s.Name, "name", info.Name, "version", info.Version)
	}
	return h, nil
}

// List returns the tools of all servers, with prefixed names, in server name
// order. A tool whose prefixed name a model API would reject is left out.
func (h *Host) List(ctx context.Context) ([]runner.ToolDef, error) {
	var out []runner.ToolDef
	for _, name := range slices.Sorted(maps.Keys(h.sessions)) {
		cs := h.sessions[name]
		n := 0
		for t, err := range cs.Tools(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("list tools of %s: %w", name, err)
			}
			full := name + Sep + t.Name
			if !validName.MatchString(full) {
				klog.Warn(klog.MsgMCPToolSkipped, "server", name, "tool", t.Name, "reason", "invalid name")
				continue
			}
			schema, err := json.Marshal(t.InputSchema)
			if err != nil {
				return nil, fmt.Errorf("schema of %s: %w", full, err)
			}
			out = append(out, runner.ToolDef{
				Name:        full,
				Description: t.Description,
				InputSchema: schema,
				Hints:       hints(t.Annotations),
			})
			n++
		}
		klog.Info(klog.MsgMCPTools, "server", name, "tools", n)
	}
	return out, nil
}

// hints converts MCP annotations, using the MCP defaults for missing ones.
func hints(a *mcp.ToolAnnotations) runner.Hints {
	if a == nil {
		return runner.Hints{Destructive: true, OpenWorld: true}
	}
	return runner.Hints{
		ReadOnly:    a.ReadOnlyHint,
		Destructive: a.DestructiveHint == nil || *a.DestructiveHint,
		Idempotent:  a.IdempotentHint,
		OpenWorld:   a.OpenWorldHint == nil || *a.OpenWorldHint,
	}
}

// Call runs a tool on its server. A failure the server reports (the tool's
// isError, or a JSON-RPC error such as an unknown tool or bad arguments) is
// returned as isError, for the model. err means the server couldn't be
// reached, e.g. its process died.
func (h *Host) Call(ctx context.Context, call runner.ToolCall) (string, bool, error) {
	server, tool, _ := strings.Cut(call.Name, Sep)
	cs, ok := h.sessions[server]
	if !ok {
		return fmt.Sprintf("unknown tool %s", call.Name), true, nil
	}
	params := &mcp.CallToolParams{Name: tool}
	if len(call.Args) > 0 {
		params.Arguments = call.Args
	}
	res, err := cs.CallTool(ctx, params)
	if err != nil {
		var rpcErr *jsonrpc.Error
		if errors.As(err, &rpcErr) {
			return rpcErr.Message, true, nil
		}
		return "", false, fmt.Errorf("mcp server %s: %w", server, err)
	}
	return text(res), res.IsError, nil
}

// text joins a result's content into the text the model sees. Text is kept;
// other content is described in brackets. Structured content is used only
// when there is no text, since servers should send it as text too.
func text(res *mcp.CallToolResult) string {
	var parts []string
	hasText := false
	for _, c := range res.Content {
		switch c := c.(type) {
		case *mcp.TextContent:
			parts = append(parts, c.Text)
			hasText = true
		case *mcp.ImageContent:
			parts = append(parts, fmt.Sprintf("[image %s, %d bytes]", c.MIMEType, len(c.Data)))
		case *mcp.AudioContent:
			parts = append(parts, fmt.Sprintf("[audio %s, %d bytes]", c.MIMEType, len(c.Data)))
		case *mcp.ResourceLink:
			parts = append(parts, fmt.Sprintf("[resource link %s]", c.URI))
		case *mcp.EmbeddedResource:
			if c.Resource != nil && c.Resource.Text != "" {
				parts = append(parts, c.Resource.Text)
				hasText = true
			} else if c.Resource != nil {
				parts = append(parts, fmt.Sprintf("[resource %s]", c.Resource.URI))
			}
		default:
			parts = append(parts, fmt.Sprintf("[%T]", c))
		}
	}
	if !hasText && res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			parts = append(parts, string(b))
		}
	}
	out := strings.Join(parts, "\n")
	if len(out) > MaxOut {
		out = out[:MaxOut] + fmt.Sprintf("\n[cut: %d of %d bytes shown]", MaxOut, len(out))
	}
	return out
}

// Close ends the sessions; each server's stdin is closed, and the process is
// stopped if it doesn't exit.
func (h *Host) Close() error {
	var errs []error
	for name, cs := range h.sessions {
		if err := cs.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close mcp server %s: %w", name, err))
		}
		delete(h.sessions, name)
	}
	return errors.Join(errs...)
}
