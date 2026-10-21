// Command demo runs a task through the runner with the SQLite store, to show
// durable execution: crash it with -crash-at, run it again, and it resumes
// without repeating finished tool calls. The model is scripted by default, or
// a real one with -model.
//
//	go run ./cmd/demo -task t1 -crash-at 7   # dies before append_file runs
//	go run ./cmd/demo -task t1               # resumes and finishes
//	go run ./cmd/demo -task t1 -log          # prints the saved steps
//	go run ./cmd/demo -task t2 -model ollama:qwen3.5:9b
//
// A fresh scripted run makes 8 calls: 1 model, 2 read_file, 3 model,
// 4 count_words, 5 count_lines, 6 model, 7 append_file, 8 model (the final
// answer). A real model may make a different number.
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

	"github.com/jrniemiec/llm"

	"github.com/jrniemiec/keel/internal/klog"
	"github.com/jrniemiec/keel/model"
	"github.com/jrniemiec/keel/runner"
	"github.com/jrniemiec/keel/store"
)

// systemPrompt is sent to real models.
const systemPrompt = `You are running a task with tools. Use the tools for every step and
do not compute results yourself. When the task is done, reply with a short summary.`

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
	level, ok := klog.ParseLevel(cfg.logLevel)
	if !ok {
		log.Fatalf("-log-level %q: want debug, info, warn or error", cfg.logLevel)
	}
	klog.Init(cfg.logFile, level, cfg.verbose)
	klog.Info(klog.MsgStarted, "cmd", "demo", "db", cfg.dbPath, "task", cfg.taskID, "model", cfg.model)
	seedNotes(cfg.dir)
	res := runTask(db, cfg)
	fmt.Printf("answer: %s (%d model calls)\n", res.Text, res.Steps)
	printReport(cfg.dir, cfg.taskID)
}

// config holds the command-line settings. -crash-at is the crashAt global,
// since maybeCrash reads it.
type config struct {
	dbPath   string
	dir      string
	taskID   string
	showLog  bool
	model    string // "scripted" or "provider:model"
	thinking bool
	logFile  string
	logLevel string
	verbose  bool
}

func parseFlags() config {
	home, err := os.UserHomeDir()
	check(err)
	var cfg config
	flag.StringVar(&cfg.dbPath, "db", filepath.Join(home, ".keel", "data", "demo.db"), "SQLite database file")
	flag.StringVar(&cfg.dir, "dir", filepath.Join(home, ".keel", "demo"), "folder for notes.txt and the reports")
	flag.StringVar(&cfg.taskID, "task", "demo", "task ID; a new ID starts a new task")
	flag.BoolVar(&cfg.showLog, "log", false, "print the task's saved steps and exit")
	flag.StringVar(&cfg.model, "model", "scripted", `"scripted", or a real model as provider:model, e.g. anthropic:claude-sonnet-5`)
	flag.BoolVar(&cfg.thinking, "thinking", false, "turn on thinking (Anthropic: adaptive; Ollama: think)")
	flag.StringVar(&cfg.logFile, "log-file", filepath.Join(home, ".keel", "keel.log"), "progress log, appended (tail -f it)")
	flag.StringVar(&cfg.logLevel, "log-level", "info", "debug, info, warn or error; debug adds prompts and tool payloads")
	flag.BoolVar(&cfg.verbose, "verbose", false, "also print the log, at debug level, to stderr")
	flag.Usage = usage
	flag.Parse()
	return cfg
}

// usage prints the help shown by -h.
func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprintf(out, `demo runs a task through the keel runner with the SQLite store.
The task reads notes.txt, counts its words and lines, and appends them to
report-<task>.txt. Crash it with -crash-at, run it again, and it resumes
without repeating finished tool calls. The model is scripted unless -model
names a real one.

Usage:
  go run ./cmd/demo [flags]

Examples:
  go run ./cmd/demo                         run task "demo" to completion
  go run ./cmd/demo -task t1 -crash-at 7    exit before append_file runs
  go run ./cmd/demo -task t1                resume t1 and finish it
  go run ./cmd/demo -task t1 -log           print t1's saved steps
  go run ./cmd/demo -task t2 -model ollama:qwen3.5:9b
  go run ./cmd/demo -task t3 -model anthropic:claude-sonnet-5 -thinking
  go run ./cmd/demo -task t4 -model openai:gpt-5

A fresh scripted run makes 8 calls: 1 model, 2 read_file, 3 model,
4 count_words, 5 count_lines, 6 model, 7 append_file, 8 model (the final
answer). A real model may make a different number; check with -log.
Use a new -task name to start over, and the same -model when resuming.
Progress goes to -log-file; follow it with: tail -f ~/.keel/keel.log

Real models need ANTHROPIC_API_KEY or OPENAI_API_KEY (not Ollama).

Flags:
`)
	flag.PrintDefaults()
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

// runTask runs the demo task with the chosen model and the file tools.
func runTask(st runner.Store, cfg config) runner.Result {
	report := reportName(cfg.taskID)
	var m runner.Model = &scriptModel{report: report}
	if cfg.model != "scripted" {
		m = model.NewLLM(llmConfigs(cfg.thinking), systemPrompt)
	}
	r := runner.Runner{Model: crashModel{m}, Tools: &fileTools{dir: cfg.dir}, Store: st}
	task := runner.Task{
		ID:           cfg.taskID,
		Goal:         "Count the words and lines in notes.txt and append them to " + report + ".",
		Model:        cfg.model,
		MaxSteps:     10,
		AllowedTools: []string{"read_file", "count_words", "count_lines", "append_file"},
	}
	res, err := r.Run(context.Background(), task)
	check(err)
	return res
}

// llmConfigs returns the provider settings for real models. API keys come
// from the environment.
func llmConfigs(thinking bool) map[string]llm.ProviderConfig {
	anthropic := llm.ProviderConfig{APIKey: os.Getenv("ANTHROPIC_API_KEY")}
	if thinking {
		anthropic.Thinking = llm.ThinkingAdaptive
	}
	return map[string]llm.ProviderConfig{
		"anthropic": anthropic,
		"openai":    {APIKey: os.Getenv("OPENAI_API_KEY")},
		"ollama":    {Think: thinking},
	}
}

// printReport prints the task's report file. A real model may not have
// written it.
func printReport(dir, taskID string) {
	report := reportName(taskID)
	out, err := os.ReadFile(filepath.Join(dir, report))
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Printf("%s: not written\n", report)
		return
	}
	check(err)
	fmt.Printf("%s:\n%s", report, out)
}

func reportName(taskID string) string { return "report-" + taskID + ".txt" }

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
