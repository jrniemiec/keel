package store

import (
	"context"
	"errors"
	"path/filepath"
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

func TestCheckSameEmptySlices(t *testing.T) {
	if err := checkSame([]byte(`{"ID":"t","AllowedTools":null}`), runner.Task{ID: "t", AllowedTools: []string{}}); err != nil {
		t.Error(err)
	}
}
