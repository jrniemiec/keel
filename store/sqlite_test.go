package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/jrniemiec/keel/runner"
)

// openDB opens a new SQLite store in a temporary folder.
func openDB(t *testing.T) *SQLite {
	t.Helper()
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "keel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// summary returns the summary of the task id.
func summary(t *testing.T, db *SQLite, id string) Summary {
	t.Helper()
	tasks, err := db.Tasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range tasks {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("task %s not in %+v", id, tasks)
	return Summary{}
}

func appendSteps(t *testing.T, db *SQLite, id string, steps ...runner.Step) {
	t.Helper()
	for _, s := range steps {
		if err := db.Append(context.Background(), id, s); err != nil {
			t.Fatal(err)
		}
	}
}

// A task's summary follows its steps: counters, what it's doing, the last
// failed tool call, and how it ended.
func TestSummaryCounters(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if err := db.StartTask(ctx, runner.Task{ID: "t1", Goal: "g", Model: "anthropic:m", MaxSteps: 5}); err != nil {
		t.Fatal(err)
	}
	if s := summary(t, db, "t1"); s.Status != runner.TaskRunning || s.Current != "starting" || s.Created.IsZero() {
		t.Errorf("started: %+v", s)
	}

	read := runner.ToolCall{ID: "c1", Name: "fs__read_file", Args: json.RawMessage(`{}`)}
	list := runner.ToolCall{ID: "c2", Name: "fs__list_directory", Args: json.RawMessage(`{}`)}
	appendSteps(t, db, "t1", runner.Step{Kind: runner.ModelReply, Msg: runner.Message{Role: "assistant",
		ToolCalls: []runner.ToolCall{read, list}, TokensIn: 100, TokensOut: 20, USD: 0.25}})
	if s := summary(t, db, "t1"); s.Current != "running tools" {
		t.Errorf("after a reply with calls: current %q", s.Current)
	}

	appendSteps(t, db, "t1", runner.Step{Kind: runner.Intent, CallID: "c1", Msg: runner.Message{ToolCalls: []runner.ToolCall{read}}})
	if s := summary(t, db, "t1"); s.Current != "calling fs__read_file" {
		t.Errorf("after an intent: current %q", s.Current)
	}

	appendSteps(t, db, "t1",
		runner.Step{Kind: runner.ToolResult, CallID: "c1", Msg: runner.Message{Role: "tool", ToolCallID: "c1", Text: "no such file", IsError: true}},
		runner.Step{Kind: runner.Intent, CallID: "c2", Msg: runner.Message{ToolCalls: []runner.ToolCall{list}}},
		runner.Step{Kind: runner.ToolResult, CallID: "c2", Msg: runner.Message{Role: "tool", ToolCallID: "c2", Text: "a.txt"}},
		runner.Step{Kind: runner.ModelReply, Msg: runner.Message{Role: "assistant", Text: "done", TokensIn: 150, TokensOut: 10, USD: 0.5}},
	)
	s := summary(t, db, "t1")
	if s.Steps != 6 || s.ModelCalls != 2 || s.ToolCalls != 2 || s.ToolErrors != 1 {
		t.Errorf("steps %d, model calls %d, tool calls %d, tool errors %d; want 6, 2, 2, 1", s.Steps, s.ModelCalls, s.ToolCalls, s.ToolErrors)
	}
	if s.TokensIn != 250 || s.TokensOut != 30 || s.CostUSD != 0.75 {
		t.Errorf("tokens %d in, %d out, $%v; want 250, 30, $0.75", s.TokensIn, s.TokensOut, s.CostUSD)
	}
	if s.LastError != "no such file" {
		t.Errorf("last error %q, want the failed call's text; a good result keeps it", s.LastError)
	}
	if s.Current != "calling fs__list_directory" {
		t.Errorf("current %q: a result or a final reply leaves it unchanged", s.Current)
	}

	if err := db.SetCurrent(ctx, "t1", "asking anthropic:m"); err != nil {
		t.Fatal(err)
	}
	if s := summary(t, db, "t1"); s.Current != "asking anthropic:m" {
		t.Errorf("after SetCurrent: current %q", s.Current)
	}
}

// FinishTask ends a task as done, with its result, or failed, with the
// error; StartTask of a finished task runs it again and clears the ending.
func TestFinishTask(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	task := runner.Task{ID: "t1", Goal: "g", Model: "ollama:m", MaxSteps: 5}
	if err := db.StartTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	appendSteps(t, db, "t1", runner.Step{Kind: runner.ToolResult, CallID: "c1", Msg: runner.Message{Text: "denied", IsError: true}})

	if err := db.FinishTask(ctx, "t1", runner.TaskDone, "3 lines", ""); err != nil {
		t.Fatal(err)
	}
	s := summary(t, db, "t1")
	if s.Status != runner.TaskDone || s.Result != "3 lines" || s.Current != "" || s.Finished.IsZero() {
		t.Errorf("done: %+v", s)
	}
	if s.LastError != "denied" {
		t.Errorf("done: last error %q, want the earlier one kept", s.LastError)
	}

	if err := db.StartTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	s = summary(t, db, "t1")
	if s.Status != runner.TaskRunning || s.Result != "" || !s.Finished.IsZero() || s.Current != "resuming" {
		t.Errorf("resumed: %+v; want running, no result, not finished", s)
	}

	if err := db.FinishTask(ctx, "t1", runner.TaskFailed, "", "step 3: ask model: 500"); err != nil {
		t.Fatal(err)
	}
	if s := summary(t, db, "t1"); s.Status != runner.TaskFailed || s.LastError != "step 3: ask model: 500" || s.Result != "" {
		t.Errorf("failed: %+v", s)
	}
}

// Steps the summary doesn't count, e.g. results_cleared, are saved and
// loaded like the others and count only as steps.
func TestOtherSteps(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if err := db.StartTask(ctx, runner.Task{ID: "t1", Goal: "g", Model: "ollama:m", MaxSteps: 5}); err != nil {
		t.Fatal(err)
	}
	cleared := runner.Step{Kind: runner.ResultsCleared, CallID: "c7", Msg: runner.Message{Text: "3 tool results, ~600 tokens"}}
	approved := runner.Step{Kind: runner.Approved, CallID: "c8", Msg: runner.Message{Text: "approved for this run"}}
	appendSteps(t, db, "t1", cleared, approved)

	steps, err := db.Load(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[0].Kind != runner.ResultsCleared || steps[0].CallID != "c7" || steps[0].Msg.Text != cleared.Msg.Text ||
		steps[1].Kind != runner.Approved || steps[1].Msg.Text != approved.Msg.Text {
		t.Errorf("loaded %+v, want both steps back", steps)
	}
	if s := summary(t, db, "t1"); s.Steps != 2 || s.ModelCalls != 0 || s.ToolCalls != 0 || s.Current != "starting" {
		t.Errorf("summary %+v, want 2 steps and nothing else", s)
	}
}

// A message survives the round trip whole, PromptTokens and Raw included.
func TestLoadMessage(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if err := db.StartTask(ctx, runner.Task{ID: "t1", Goal: "g", Model: "anthropic:m", MaxSteps: 5}); err != nil {
		t.Fatal(err)
	}
	reply := runner.Message{
		Role: "assistant", Text: "reading", Stop: runner.StopToolUse,
		ToolCalls: []runner.ToolCall{{ID: "c1", Name: "fs__read_file", Args: json.RawMessage(`{"path":"a.txt"}`)}},
		Raw:       json.RawMessage(`[{"type":"text","text":"reading"}]`), RawFrom: "anthropic",
		TokensIn: 100, TokensOut: 20, PromptTokens: 100, USD: 0.25, Model: "anthropic:m",
	}
	appendSteps(t, db, "t1", runner.Step{Kind: runner.ModelReply, Msg: reply})
	steps, err := db.Load(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(steps[0].Msg)
	want, _ := json.Marshal(reply)
	if string(got) != string(want) {
		t.Errorf("loaded\n%s\nwant\n%s", got, want)
	}
}

// A tool call has at most one result: a second one is refused, so a call is
// never recorded as run twice.
func TestOneResultPerCall(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if err := db.StartTask(ctx, runner.Task{ID: "t1", Goal: "g", Model: "ollama:m", MaxSteps: 5}); err != nil {
		t.Fatal(err)
	}
	result := runner.Step{Kind: runner.ToolResult, CallID: "c1", Msg: runner.Message{Role: "tool", ToolCallID: "c1", Text: "ok"}}
	appendSteps(t, db, "t1", result)
	if err := db.Append(ctx, "t1", result); err == nil {
		t.Fatal("second result for c1: want an error")
	}
	if s := summary(t, db, "t1"); s.Steps != 1 || s.ToolCalls != 1 {
		t.Errorf("summary %+v: the refused step must change nothing", s)
	}
	// Another task may use the same call ID.
	if err := db.StartTask(ctx, runner.Task{ID: "t2", Goal: "g", Model: "ollama:m", MaxSteps: 5}); err != nil {
		t.Fatal(err)
	}
	appendSteps(t, db, "t2", result)
}

// Tasks lists the most recently updated first.
func TestTasksOrder(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	for _, id := range []string{"t1", "t2"} {
		if err := db.StartTask(ctx, runner.Task{ID: id, Goal: "g", Model: "ollama:m", MaxSteps: 5}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(time.Millisecond) // timestamps have nanoseconds; make sure t1's is later
	if err := db.SetCurrent(ctx, "t1", "asking"); err != nil {
		t.Fatal(err)
	}
	tasks, err := db.Tasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 || tasks[0].ID != "t1" || tasks[1].ID != "t2" {
		t.Errorf("tasks = %v, %v; want t1 (updated last), then t2", tasks[0].ID, tasks[len(tasks)-1].ID)
	}
}

// An unknown task in Load has no steps; one in OpenSQLite's path that can't
// be created is an error.
func TestEdgeCases(t *testing.T) {
	db := openDB(t)
	steps, err := db.Load(context.Background(), "missing")
	if err != nil || len(steps) != 0 {
		t.Errorf("Load(missing) = %v, %v; want no steps", steps, err)
	}
	if _, err := OpenSQLite(filepath.Join(t.TempDir(), "no", "such", "dir", "keel.db")); err == nil {
		t.Error("OpenSQLite in a missing folder: want an error")
	}
}
