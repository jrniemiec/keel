package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"

	"github.com/jrniemiec/keel/internal/klog"
	"github.com/jrniemiec/keel/runner"
	"github.com/jrniemiec/keel/store"
)

var version = "dev" // set by the Makefile

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
	if len(os.Args) > 1 && os.Args[1] == "status" {
		statusCmd(os.Args[2:])
		return
	}

	dbPath := flag.String("db", defaultPath("data", "keel.db"), "SQLite database file")
	mem := flag.Bool("mem", false, "use the in-memory store instead of SQLite")
	logFile := flag.String("log-file", defaultPath("keel.log"), "progress log, appended (tail -f it)")
	logLevel := flag.String("log-level", "info", "debug, info, warn or error")
	verbose := flag.Bool("verbose", false, "also print the log, at debug level, to stderr")
	flag.Usage = usage
	flag.Parse()

	level, ok := klog.ParseLevel(*logLevel)
	if !ok {
		log.Fatalf("-log-level %q: want debug, info, warn or error", *logLevel)
	}
	klog.Init(*logFile, level, *verbose)
	klog.Info(klog.MsgStarted, "version", version, "db", *dbPath, "mem", *mem)

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

// usage prints the help shown by -h.
func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprintf(out, `keel runs an agent task (for now, a stub model and tool) with a durable step log.

Usage:
  keel [flags]
  keel status [-w] [-db file]   show tasks and their summaries (see keel status -h)

Examples:
  keel                 run task "1" with SQLite at the default path
  keel -db /tmp/k.db   use another database file
  keel -mem            use the in-memory store (nothing is saved)
  keel status -w       watch all tasks, refreshed every second
  keel status -db ~/.keel/data/demo.db   the demo's tasks

Task "1" finishes on the first run; later runs return its saved answer.
For a crash-and-resume demo, see: go run ./cmd/demo -h

Flags:
`)
	flag.PrintDefaults()
}

// defaultPath returns ~/.keel/<elem…>, e.g. ~/.keel/data/keel.db.
func defaultPath(elem ...string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return elem[len(elem)-1] // fall back to the current directory
	}
	return filepath.Join(append([]string{home, ".keel"}, elem...)...)
}
