package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jrniemiec/keel/runner"
)

// fileTools are the demo's tools. File paths are kept inside dir.
type fileTools struct {
	dir string
}

func (t *fileTools) List(ctx context.Context) ([]runner.ToolDef, error) {
	return []runner.ToolDef{
		{Name: "read_file", Description: "Read a file in the demo folder.", InputSchema: schema("path")},
		{Name: "count_words", Description: "Count the words in text.", InputSchema: schema("text")},
		{Name: "count_lines", Description: "Count the lines in text.", InputSchema: schema("text")},
		{Name: "append_file", Description: "Append text to a file in the demo folder.", InputSchema: schema("path", "text")},
	}, nil
}

func (t *fileTools) Call(ctx context.Context, call runner.ToolCall) (string, error) {
	maybeCrash(call.Name)
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
