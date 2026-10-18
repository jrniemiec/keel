package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strconv"

	"github.com/jrniemiec/keel/runner"
	"github.com/jrniemiec/keel/store"
)

type stubModel struct{}

func (stubModel) Ask(ctx context.Context, model string, msgs []runner.Message, tools []runner.ToolDef) (runner.Message, error) {
	return runner.Message{Role: "assistant", Text: "llm respnonse"}, nil
}

type stubTools struct{}

func (stubTools) List(ctx context.Context) ([]runner.ToolDef, error) {
	return []runner.ToolDef{{Name: "echo", Description: "desc1"}}, nil
}
func (stubTools) Call(ctx context.Context, call runner.ToolCall) (string, error) {
	return "", nil
}

func main() {
	dbPath := flag.String("db", defaultDBPath(), "SQLite database file")
	mem := flag.Bool("mem", false, "use the in-memory store instead of SQLite")
	flag.Parse()

	var st runner.Store = store.NewMemory()
	if !*mem {
		if err := os.MkdirAll(filepath.Dir(*dbPath), 0o755); err != nil {
			panic(err)
		}
		db, err := store.OpenSQLite(*dbPath)
		if err != nil {
			panic(err)
		}
		defer db.Close()
		st = db
	}

	r := runner.Runner{Model: stubModel{}, Tools: stubTools{}, Store: st}
	result, err := r.Run(context.Background(), runner.Task{ID: "1", Goal: "say hello", Model: "stub", MaxSteps: 10, AllowedTools: []string{"echo"}})
	if err != nil {
		panic(err)
	}
	println(result.Text + " in steps: " + strconv.Itoa(result.Steps))
}

// defaultDBPath returns ~/.keel/data/keel.db.
func defaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "keel.db" // fall back to the current directory
	}
	return filepath.Join(home, ".keel", "data", "keel.db")
}
