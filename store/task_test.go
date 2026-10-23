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

			changed := task
			changed.Model = "anthropic:m"
			changed.Thinking = true
			err := st.StartTask(ctx, changed)
			if !errors.Is(err, runner.ErrTaskChanged) {
				t.Fatalf("err = %v, want ErrTaskChanged", err)
			}
			if !strings.Contains(err.Error(), "Model, Thinking") {
				t.Errorf("err = %v, want it to name Model and Thinking", err)
			}
		})
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
