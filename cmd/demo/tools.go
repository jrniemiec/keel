package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jrniemiec/keel/mcphost"
	"github.com/jrniemiec/keel/runner"
)

// demoTools offers the demo's own tools and, with -fs, the filesystem MCP
// server's (named fs__<tool>). -crash-at counts its calls.
type demoTools struct {
	own *fileTools
	mcp *mcphost.Host // nil without -fs
}

func (t demoTools) List(ctx context.Context) ([]runner.ToolDef, error) {
	defs, err := t.own.List(ctx)
	if err != nil || t.mcp == nil {
		return defs, err
	}
	more, err := t.mcp.List(ctx)
	return append(defs, more...), err
}

func (t demoTools) Call(ctx context.Context, call runner.ToolCall) (string, bool, error) {
	maybeCrash(call.Name)
	if t.mcp != nil && strings.HasPrefix(call.Name, "fs"+mcphost.Sep) {
		return t.mcp.Call(ctx, call)
	}
	return t.own.Call(ctx, call)
}

// fileTools are the demo's tools. File paths are kept inside dir.
type fileTools struct {
	dir string
}

func (t *fileTools) List(ctx context.Context) ([]runner.ToolDef, error) {
	read := runner.Hints{ReadOnly: true}
	return []runner.ToolDef{
		{Name: "read_file", Description: "Read a file in the demo folder.", InputSchema: schema("path"), Hints: read},
		{Name: "count_words", Description: "Count the words in text.", InputSchema: schema("text"), Hints: read},
		{Name: "count_lines", Description: "Count the lines in text.", InputSchema: schema("text"), Hints: read},
		// Appending twice appends twice: not idempotent.
		{Name: "append_file", Description: "Append text to a file in the demo folder.", InputSchema: schema("path", "text")},
	}, nil
}

// Call runs a tool. A tool's own failure (bad args, missing file) is an
// isError result; the demo tools never return err.
func (t *fileTools) Call(ctx context.Context, call runner.ToolCall) (string, bool, error) {
	out, err := t.run(call)
	if err != nil {
		return err.Error(), true, nil
	}
	return out, false, nil
}

func (t *fileTools) run(call runner.ToolCall) (string, error) {
	var args struct{ Path, Text string }
	if err := json.Unmarshal(call.Args, &args); err != nil {
		return "", fmt.Errorf("bad args: %w", err)
	}
	path := filepath.Join(t.dir, filepath.Base(args.Path))

	switch call.Name {
	case "read_file":
		b, err := os.ReadFile(path)
		return string(b), err
	case "count_words":
		return strconv.Itoa(len(strings.Fields(args.Text))), nil
	case "count_lines":
		return strconv.Itoa(strings.Count(args.Text, "\n")), nil
	case "append_file":
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return "", err
		}
		defer f.Close()
		if _, err := f.WriteString(args.Text); err != nil {
			return "", err
		}
		return "ok", nil
	}
	return "", fmt.Errorf("unknown tool %s", call.Name)
}

// schema returns a JSON Schema for an object with the given string properties.
func schema(props ...string) json.RawMessage {
	p := map[string]any{}
	for _, name := range props {
		p[name] = map[string]string{"type": "string"}
	}
	b, _ := json.Marshal(map[string]any{"type": "object", "properties": p, "required": props})
	return b
}
