// Command demo runs a scripted task through the runner with the SQLite store,
// to show durable execution: crash it with -crash-at, run it again, and it
// resumes without repeating finished tool calls.
//
//	go run ./cmd/demo -task t1 -crash-at 7   # dies before append_file runs
//	go run ./cmd/demo -task t1               # resumes and finishes
//	go run ./cmd/demo -task t1 -log          # prints the saved steps
//
// A fresh run makes 8 calls: 1 model, 2 read_file, 3 model, 4 count_words,
// 5 count_lines, 6 model, 7 append_file, 8 model (the final answer).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/jrniemiec/keel/runner"
	"github.com/jrniemiec/keel/store"
)

const sampleNotes = `keel is a durable agent runtime.
Every step goes into an append-only log.
A crashed task resumes from its last step.
`

func main() {
	cfg := parseFlags()

	db := openStore(cfg.dbPath)
	defer db.Close()

	if cfg.showLog {
		printLog(db, cfg.taskID)
		return
	}
	seedNotes(cfg.dir)
	res := runTask(db, cfg)
	fmt.Printf("answer: %s (%d model calls)\n", res.Text, res.Steps)
	printReport(cfg.dir, cfg.taskID)
}

// config holds the command-line settings. -crash-at is the crashAt global,
// since maybeCrash reads it.
type config struct {
	dbPath  string
	dir     string
	taskID  string
	showLog bool
}

func parseFlags() config {
	home, err := os.UserHomeDir()
	check(err)
	var cfg config
	flag.StringVar(&cfg.dbPath, "db", filepath.Join(home, ".keel", "data", "demo.db"), "SQLite database file")
	flag.StringVar(&cfg.dir, "dir", filepath.Join(home, ".keel", "demo"), "folder for notes.txt and the reports")
	flag.StringVar(&cfg.taskID, "task", "demo", "task ID; a new ID starts a new task")
	flag.BoolVar(&cfg.showLog, "log", false, "print the task's saved steps and exit")
	flag.Parse()
	return cfg
}

// openStore creates the database folder and opens the SQLite store.
func openStore(path string) *store.SQLite {
	check(os.MkdirAll(filepath.Dir(path), 0o755))
	db, err := store.OpenSQLite(path)
	check(err)
	return db
}

// seedNotes writes notes.txt into dir unless it already exists.
func seedNotes(dir string) {
	check(os.MkdirAll(dir, 0o755))
	notesPath := filepath.Join(dir, "notes.txt")
	if _, err := os.Stat(notesPath); errors.Is(err, fs.ErrNotExist) {
		check(os.WriteFile(notesPath, []byte(sampleNotes), 0o644))
	}
}

// runTask runs the demo task with the scripted model and the file tools.
func runTask(st runner.Store, cfg config) runner.Result {
	report := reportName(cfg.taskID)
	r := runner.Runner{Model: &scriptModel{report: report}, Tools: &fileTools{dir: cfg.dir}, Store: st}
	task := runner.Task{
		ID:           cfg.taskID,
		Goal:         "Count the words and lines in notes.txt and append them to " + report + ".",
		Model:        "scripted",
		MaxSteps:     10,
		AllowedTools: []string{"read_file", "count_words", "count_lines", "append_file"},
	}
	res, err := r.Run(context.Background(), task)
	check(err)
	return res
}

// printReport prints the task's report file.
func printReport(dir, taskID string) {
	report := reportName(taskID)
	out, err := os.ReadFile(filepath.Join(dir, report))
	check(err)
	fmt.Printf("%s:\n%s", report, out)
}

func reportName(taskID string) string { return "report-" + taskID + ".txt" }

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
