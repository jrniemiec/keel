// Package mcphost runs MCP servers as child processes over stdio and offers
// their tools to the runner: Host implements runner.Tools. A tool is named
// <server>__<tool>, e.g. "fs__read_file".
package mcphost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jrniemiec/keel/internal/klog"
	"github.com/jrniemiec/keel/runner"
)

// Sep joins a server's name and its tool's name.
const Sep = "__"

// validName is what Anthropic and OpenAI accept as a tool name.
var validName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// Server is an MCP server started as a child process.
type Server struct {
	Name    string   // prefixes its tools; letters, digits, '-' or '_', no "__"
	Command string   // e.g. "mcp-filesystem-server"
	Args    []string // e.g. the directories it may access
	Env     []string // "KEY=value", added to keel's environment
	Hints   []HintOverride
}

// HintOverride corrects the hints a server gives the tools that Tools
// matches: the tool's own name, without the server prefix, or a path.Match
// pattern. A nil hint keeps the server's value. Overrides apply in order.
type HintOverride struct {
	Tools       string `json:"tools"` // e.g. "read_*"
	ReadOnly    *bool  `json:"read_only"`
	Destructive *bool  `json:"destructive"`
	Idempotent  *bool  `json:"idempotent"`
	OpenWorld   *bool  `json:"open_world"`
}

// apply returns hints with o's non-nil hints set.
func (o HintOverride) apply(hints runner.Hints) runner.Hints {
	set := func(dst *bool, src *bool) {
		if src != nil {
			*dst = *src
		}
	}
	set(&hints.ReadOnly, o.ReadOnly)
	set(&hints.Destructive, o.Destructive)
	set(&hints.Idempotent, o.Idempotent)
	set(&hints.OpenWorld, o.OpenWorld)
	return hints
}

// withOverrides returns serverHints of tool with the overrides that match
// it applied in order, and marks each matching override in used.
func withOverrides(tool string, serverHints runner.Hints, overrides []HintOverride, used []bool) runner.Hints {
	hints := serverHints
	for i, o := range overrides {
		if ok, _ := path.Match(o.Tools, tool); ok {
			hints = o.apply(hints)
			used[i] = true
		}
	}
	return hints
}

// Host holds a session with each started server. All maps are by server
// name. It is used by one goroutine at a time.
type Host struct {
	client    *mcp.Client
	servers   map[string]Server             // how to start each server, also for a restart
	sessions  map[string]*mcp.ClientSession // none while a server is down and not restarted
	stderr    map[string]*stderrLog         // each session's stderr
	down      map[string]bool               // died; restarted before the next call
	restarted map[string]bool               // restarted once; a second death is final
	maxOut    int                           // bytes of one tool result kept
}

// Start starts the servers and connects to them. maxOut caps the text of one
// tool result, so a large file read can't fill the model's context. On error,
// the servers already started are closed.
func Start(ctx context.Context, servers []Server, maxOut int) (*Host, error) {
	h := &Host{
		client:    mcp.NewClient(&mcp.Implementation{Name: "keel", Version: "dev"}, nil),
		servers:   map[string]Server{},
		sessions:  map[string]*mcp.ClientSession{},
		stderr:    map[string]*stderrLog{},
		down:      map[string]bool{},
		restarted: map[string]bool{},
		maxOut:    maxOut,
	}
	for _, s := range servers {
		if !validName.MatchString(s.Name) || strings.Contains(s.Name, Sep) {
			h.Close()
			return nil, fmt.Errorf("mcp server name %q: want letters, digits, '-' or '_', no %q", s.Name, Sep)
		}
		if _, dup := h.servers[s.Name]; dup {
			h.Close()
			return nil, fmt.Errorf("mcp server %q listed twice", s.Name)
		}
		h.servers[s.Name] = s
		if err := h.connect(ctx, s); err != nil {
			h.Close()
			return nil, fmt.Errorf("start mcp server %s: %w", s.Name, err)
		}
	}
	return h, nil
}

// connect starts server s and connects to it.
func (h *Host) connect(ctx context.Context, s Server) error {
	cmd := exec.Command(s.Command, s.Args...)
	cmd.Env = append(os.Environ(), s.Env...)
	// CommandTransport takes stdin and stdout and leaves Stderr as set
	// here; exec copies the server's stderr into it.
	stderr := &stderrLog{server: s.Name}
	cmd.Stderr = stderr
	cs, err := h.client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		stderr.flush() // why the server failed to start, if it said so
		return err
	}
	h.sessions[s.Name], h.stderr[s.Name] = cs, stderr
	info := cs.InitializeResult().ServerInfo
	klog.Info(klog.MsgMCPUp, "server", s.Name, "name", info.Name, "version", info.Version)
	return nil
}

// restart starts server name again after it died, once per Host.
func (h *Host) restart(ctx context.Context, name string) error {
	if h.restarted[name] {
		return fmt.Errorf("mcp server %s: %w: restarted once already", name, runner.ErrServerDown)
	}
	h.restarted[name] = true
	klog.Warn(klog.MsgMCPRestart, "server", name)
	h.closeSession(name) // reaps the dead process and flushes its stderr
	if err := h.connect(ctx, h.servers[name]); err != nil {
		return fmt.Errorf("restart mcp server %s: %w: %w", name, runner.ErrServerDown, err)
	}
	h.down[name] = false
	return nil
}

// List returns the tools of all servers, with prefixed names, in server name
// order. A tool whose prefixed name a model API would reject is left out.
func (h *Host) List(ctx context.Context) ([]runner.ToolDef, error) {
	var out []runner.ToolDef
	for _, name := range slices.Sorted(maps.Keys(h.sessions)) {
		cs := h.sessions[name]
		overrides := h.servers[name].Hints
		used := make([]bool, len(overrides))
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
			serverHints := hints(t.Annotations)
			out = append(out, runner.ToolDef{
				Name:        full,
				Description: t.Description,
				InputSchema: schema,
				Hints:       withOverrides(t.Name, serverHints, overrides, used),
				ServerHints: serverHints,
			})
			n++
		}
		klog.Info(klog.MsgMCPTools, "server", name, "tools", n)
		for i, o := range overrides {
			if !used[i] {
				klog.Warn(klog.MsgMCPHintUnused, "server", name, "tools", o.Tools)
			}
		}
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
// reached; it wraps runner.ErrServerDown when the server died, and the next
// Call to that server restarts it, once.
func (h *Host) Call(ctx context.Context, call runner.ToolCall) (string, bool, error) {
	server, tool, _ := strings.Cut(call.Name, Sep)
	if _, ok := h.servers[server]; !ok {
		return fmt.Sprintf("unknown tool %s", call.Name), true, nil
	}
	if h.down[server] {
		if err := h.restart(ctx, server); err != nil {
			return "", false, err
		}
	}
	cs := h.sessions[server]
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
		if ctx.Err() != nil {
			return "", false, fmt.Errorf("mcp server %s: %w", server, err)
		}
		// Any other error: the server is unreachable, and whether the call
		// ran is unknown (go-sdk doesn't tell "never sent" from "died").
		h.down[server] = true
		klog.Warn(klog.MsgMCPDown, "server", server, "tool", tool, "err", err)
		return "", false, fmt.Errorf("mcp server %s: %w: %w", server, runner.ErrServerDown, err)
	}
	return text(res, h.maxOut), res.IsError, nil
}

// text joins a result's content into the text the model sees. Text is kept;
// other content is described in brackets. Structured content is used only
// when there is no text, since servers should send it as text too. Text
// longer than maxOut bytes is cut, before a character that would be split.
func text(res *mcp.CallToolResult, maxOut int) string {
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
	if len(out) > maxOut {
		cut := maxOut
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = out[:cut] + fmt.Sprintf("\n[cut: %d of %d bytes shown]", cut, len(out))
	}
	return out
}

// Close ends the sessions; each server's stdin is closed, and the process is
// stopped if it doesn't exit. Then a last stderr line without '\n' is logged.
func (h *Host) Close() error {
	var errs []error
	for name := range h.sessions {
		if err := h.closeSession(name); err != nil && !h.down[name] { // a dead one fails to close
			errs = append(errs, fmt.Errorf("close mcp server %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// closeSession ends server name's session, waits for its process, and logs
// a last stderr line without '\n'.
func (h *Host) closeSession(name string) error {
	cs, ok := h.sessions[name]
	if !ok {
		return nil
	}
	err := cs.Close()
	delete(h.sessions, name)
	if stderr := h.stderr[name]; stderr != nil {
		stderr.flush() // the process has exited (cmd.Wait): no more writes
		delete(h.stderr, name)
	}
	return err
}

// maxStderrLine is the bytes of one stderr line kept in the log.
const maxStderrLine = 2000

// stderrLog is a server's stderr: it logs each line as an mcp stderr entry.
// exec copies the process's stderr into Write from one goroutine, so it
// needs no lock.
type stderrLog struct {
	server  string
	log     *klog.Logger // nil = klog.Default()
	partial []byte       // a line not yet ended by '\n'
}

func (w *stderrLog) Write(p []byte) (int, error) {
	w.partial = append(w.partial, p...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		w.logLine(w.partial[:i])
		w.partial = w.partial[i+1:]
	}
	if len(w.partial) > maxStderrLine { // a long line without '\n': log what there is
		w.logLine(w.partial)
		w.partial = nil
	}
	return len(p), nil
}

// flush logs a last line without '\n'; called after the process exits.
func (w *stderrLog) flush() {
	if len(w.partial) > 0 {
		w.logLine(w.partial)
		w.partial = nil
	}
}

func (w *stderrLog) logLine(line []byte) {
	line = bytes.TrimRight(line, "\r")
	if len(line) == 0 {
		return
	}
	text := string(line)
	if len(text) > maxStderrLine {
		text = text[:maxStderrLine] + "…"
	}
	log := w.log
	if log == nil {
		log = klog.Default()
	}
	log.Info(klog.MsgMCPStderr, "server", w.server, "line", text)
}
