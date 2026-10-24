package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jrniemiec/keel/runner"
)

func TestStartTaskChanged(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "keel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	stores := map[string]runner.Store{"memory": NewMemory(), "sqlite": db}
	for name, st := range stores {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			task := runner.Task{ID: "t1", Goal: "g", Model: "ollama:m", MaxSteps: 5, AllowedTools: []string{"fs__read_file"}}
			if err := st.StartTask(ctx, task); err != nil {
				t.Fatal(err)
			}
			if err := st.StartTask(ctx, task); err != nil {
				t.Fatalf("resume with the same settings: %v", err)
			}

			switched := task
			switched.Model = "anthropic:m"
			switched.Thinking = true
			if err := st.StartTask(ctx, switched); err != nil {
				t.Fatalf("resume with another model: %v", err)
			}

			changed := task
			changed.Goal = "g2"
			changed.MaxSteps = 6
			err := st.StartTask(ctx, changed)
			if !errors.Is(err, runner.ErrTaskChanged) {
				t.Fatalf("err = %v, want ErrTaskChanged", err)
			}
			if !strings.Contains(err.Error(), "Goal, MaxSteps") {
				t.Errorf("err = %v, want it to name Goal and MaxSteps", err)
			}
		})
	}
}

func TestStartTaskSavesSwitchedModel(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "keel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	task := runner.Task{ID: "t1", Goal: "g", Model: "anthropic:m", MaxSteps: 5}
	if err := db.StartTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	task.Model = "openai:m"
	if err := db.StartTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	tasks, err := db.Tasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Model != "openai:m" {
		t.Errorf("tasks = %+v, want model openai:m", tasks)
	}
}

func TestToolSnapshotStep(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "keel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	tools := []runner.ToolDef{{
		Name:        "fs__read_file",
		Description: "Read a file",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Hints:       runner.Hints{ReadOnly: true, Idempotent: true},
	}}
	stores := map[string]runner.Store{"memory": NewMemory(), "sqlite": db}
	for name, st := range stores {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if err := st.StartTask(ctx, runner.Task{ID: "t1", Goal: "g", Model: "ollama:m", MaxSteps: 5}); err != nil {
				t.Fatal(err)
			}
			if err := st.Append(ctx, "t1", runner.Step{Kind: runner.ToolSnapshot, Tools: tools}); err != nil {
				t.Fatal(err)
			}
			steps, err := st.Load(ctx, "t1")
			if err != nil {
				t.Fatal(err)
			}
			if len(steps) != 1 || steps[0].Kind != runner.ToolSnapshot || !reflect.DeepEqual(steps[0].Tools, tools) {
				t.Errorf("steps = %+v, want the snapshot back", steps)
			}
		})
	}
}

func TestBlockTask(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "keel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	task := runner.Task{ID: "t1", Goal: "g", Model: "ollama:m", MaxSteps: 5}
	status := func() Summary {
		t.Helper()
		tasks, err := db.Tasks(ctx)
		if err != nil || len(tasks) != 1 {
			t.Fatalf("tasks = %v, %v", tasks, err)
		}
		return tasks[0]
	}

	if err := db.BlockTask(ctx, "missing", "x"); err != nil {
		t.Fatalf("block a missing task: %v", err)
	}
	if err := db.StartTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := db.BlockTask(ctx, "t1", "tool server down"); err != nil {
		t.Fatal(err)
	}
	if s := status(); s.Status != runner.TaskWaiting || s.LastError != "tool server down" || s.Current != "blocked" || !s.Finished.IsZero() {
		t.Errorf("after BlockTask: %+v", s)
	}
	if err := db.StartTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if s := status(); s.Status != runner.TaskRunning {
		t.Errorf("after StartTask: status %q, want running", s.Status)
	}
}

func TestCheckSameEmptySlices(t *testing.T) {
	if err := checkSame([]byte(`{"ID":"t","AllowedTools":null}`), runner.Task{ID: "t", AllowedTools: []string{}}); err != nil {
		t.Error(err)
	}
}

func TestCheckSameApproval(t *testing.T) {
	ask := runner.Approval{Read: "auto", Write: "ask", Destructive: "ask"}
	if err := checkSame([]byte(`{"ID":"t"}`), runner.Task{ID: "t", Approval: ask}); err != nil {
		t.Errorf("task saved before approvals: %v", err)
	}
	saved := []byte(`{"ID":"t","Approval":{"read":"auto","write":"ask","destructive":"ask"}}`)
	if err := checkSame(saved, runner.Task{ID: "t", Approval: ask}); err != nil {
		t.Errorf("same approval: %v", err)
	}
	auto := ask
	auto.Destructive = "auto"
	if err := checkSame(saved, runner.Task{ID: "t", Approval: auto}); !errors.Is(err, runner.ErrTaskChanged) || !strings.Contains(err.Error(), "Approval") {
		t.Errorf("changed approval: err = %v, want ErrTaskChanged naming Approval", err)
	}
}

func TestSummarizeApprovalAsked(t *testing.T) {
	c := runner.ToolCall{ID: "c1", Name: "fs__write_file"}
	d := summarize(runner.Step{Kind: runner.ApprovalAsked, CallID: "c1", Msg: runner.Message{Text: "destructive", ToolCalls: []runner.ToolCall{c}}})
	if d.current != "waiting for approval: fs__write_file" || d.toolCalls != 0 {
		t.Errorf("delta = %+v", d)
	}
	if d := summarize(runner.Step{Kind: runner.Approved, CallID: "c1"}); d != (delta{}) {
		t.Errorf("approved: delta = %+v, want none", d)
	}
}
