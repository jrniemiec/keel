package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jrniemiec/keel/runner"
	"github.com/jrniemiec/keel/store"
)

// testDB saves two tasks: t1 done after a tool call, t2 failed with no steps.
func testDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keel.db")
	db, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	call := runner.ToolCall{ID: "c1", Name: "fs__read_file", Args: json.RawMessage(`{"path":"notes.txt"}`)}
	for _, task := range []runner.Task{{ID: "t1", Model: "openai:m"}, {ID: "t2", Model: "openai:m"}} {
		if err := db.StartTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []runner.Step{
		{Kind: runner.ToolSnapshot, Tools: []runner.ToolDef{{Name: "fs__read_file"}}},
		{Kind: runner.ModelReply, Msg: runner.Message{Role: "assistant", ToolCalls: []runner.ToolCall{call}, Stop: runner.StopToolUse, Model: "openai:m"}},
		{Kind: runner.Intent, CallID: "c1", Msg: runner.Message{ToolCalls: []runner.ToolCall{call}}},
		{Kind: runner.ToolResult, CallID: "c1", Msg: runner.Message{Role: "tool", ToolCallID: "c1", Text: "no such file", IsError: true}},
		{Kind: runner.ModelReply, Msg: runner.Message{Role: "assistant", Text: "The file is missing.\nNothing to count.", Stop: runner.StopEnd, Model: "openai:m"}},
	} {
		if err := db.Append(ctx, "t1", s); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.FinishTask(ctx, "t1", runner.TaskDone, "The file is missing.\nNothing to count.", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishTask(ctx, "t2", runner.TaskFailed, "", "step 0: ask model: 400"); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSteps(t *testing.T) {
	db := testDB(t)
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"short", []string{"-d", db, "t1"}, []string{
			"SEQ  KIND           DETAIL",
			"1    tool_snapshot  1 tools",
			"2    model_reply    openai:m tool_use: fs__read_file",
			"3    intent         fs__read_file",
			`4    tool_result    fs__read_file error "no such file"`,
			`5    model_reply    openai:m end: "The file is missing. Nothing to count."`,
		}},
		{"full", []string{"t1", "--full", "-d", db}, []string{
			"3    intent         fs__read_file",
			`    {"path":"notes.txt"}`,
			"5    model_reply    openai:m end",
			"    The file is missing.",
			"    Nothing to count.",
		}},
		{"no steps", []string{"-d", db, "t2"}, []string{"no steps"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := steps(tt.args, &out); err != nil {
				t.Fatal(err)
			}
			for _, w := range tt.want {
				if !strings.Contains(out.String(), w+"\n") {
					t.Errorf("output lacks %q:\n%s", w, out.String())
				}
			}
		})
	}
}

func TestStepsErrors(t *testing.T) {
	db := testDB(t)
	var ue usageError
	if err := steps([]string{"-d", db}, &bytes.Buffer{}); !errors.As(err, &ue) {
		t.Errorf("no task ID: err = %v, want a usage error", err)
	}
	if err := steps([]string{"-d", db, "nope"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `no task "nope"`) {
		t.Errorf("unknown task: err = %v", err)
	}
	if err := steps([]string{"-d", filepath.Join(t.TempDir(), "none.db"), "t1"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "no database at") {
		t.Errorf("missing db: err = %v", err)
	}
}

func TestTasks(t *testing.T) {
	db := testDB(t)
	tests := []struct {
		name          string
		args          []string
		want, notWant []string
	}{
		{"all", []string{"-d", db}, []string{"NOTE", "t1    done", "The file is missing. Nothing to count.", "t2    failed", "step 0: ask model: 400"}, nil},
		{"status", []string{"-d", db, "-s", "failed"}, []string{"t2"}, []string{"t1"}},
		{"full", []string{"-d", db, "--full", "--status", "done"}, []string{"NOTE", "The file is missing. Nothing to count.\n"}, []string{"t2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := tasks(tt.args, &out); err != nil {
				t.Fatal(err)
			}
			for _, w := range tt.want {
				if !strings.Contains(out.String(), w) {
					t.Errorf("output lacks %q:\n%s", w, out.String())
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(out.String(), w) {
					t.Errorf("output has %q:\n%s", w, out.String())
				}
			}
		})
	}

	var ue usageError
	if err := tasks([]string{"-d", db, "-s", "finished"}, &bytes.Buffer{}); !errors.As(err, &ue) {
		t.Errorf("bad status: err = %v, want a usage error", err)
	}
}

func TestStepsResultsCleared(t *testing.T) {
	call := runner.ToolCall{ID: "c1", Name: "fs__read_file", Args: json.RawMessage(`{}`)}
	history := []runner.Step{
		{Kind: runner.Intent, CallID: "c1", Msg: runner.Message{ToolCalls: []runner.ToolCall{call}}},
		{Kind: runner.ResultsCleared, CallID: "c1", Msg: runner.Message{Text: "12 tool results, ~40K tokens"}},
	}
	var out strings.Builder
	if err := printSteps(&out, history, false); err != nil {
		t.Fatal(err)
	}
	if want := "results_cleared  12 tool results, ~40K tokens, through fs__read_file"; !strings.Contains(out.String(), want) {
		t.Errorf("got:\n%s\nwant a line with %q", out.String(), want)
	}
}
