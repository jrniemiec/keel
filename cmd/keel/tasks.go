package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/jrniemiec/keel/runner"
	"github.com/jrniemiec/keel/store"
)

// showFlags holds the flags of keel tasks and keel steps.
type showFlags struct {
	config, db, status string
	full, help         bool
}

// newShowFlags defines the flags of keel tasks (withStatus) or keel steps.
// They are described in helpText.
func newShowFlags(name string, withStatus bool) (*flag.FlagSet, *showFlags) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are reported by main, help by helpText
	f := &showFlags{}
	for _, n := range []string{"c", "config"} {
		fs.StringVar(&f.config, n, "", "")
	}
	for _, n := range []string{"d", "db"} {
		fs.StringVar(&f.db, n, "", "")
	}
	if withStatus {
		for _, n := range []string{"s", "status"} {
			fs.StringVar(&f.status, n, "", "")
		}
	}
	fs.BoolVar(&f.full, "full", false, "")
	for _, n := range []string{"h", "help"} {
		fs.BoolVar(&f.help, n, false, "")
	}
	return fs, f
}

var taskStatuses = []string{runner.TaskRunning, runner.TaskWaiting, runner.TaskFailed, runner.TaskDone}

// tasks is "keel tasks": it lists the tasks in the database once.
func tasks(args []string, w io.Writer) error {
	flags, f := newShowFlags("tasks", true)
	rest, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	if f.help {
		fmt.Fprint(w, helpText)
		return nil
	}
	if len(rest) > 0 {
		return usageError{fmt.Sprintf("keel tasks takes no arguments, got %q", rest[0])}
	}
	if f.status != "" && !slices.Contains(taskStatuses, f.status) {
		return usageError{fmt.Sprintf("--status %q: want running, waiting, failed or done", f.status)}
	}

	db, err := openDB(f.config, f.db)
	if err != nil {
		return err
	}
	defer db.Close()
	all, err := db.Tasks(context.Background())
	if err != nil {
		return err
	}
	var shown []store.Summary
	for _, t := range all {
		if f.status == "" || t.Status == f.status {
			shown = append(shown, t)
		}
	}
	return printTasks(w, shown, time.Now(), f.full)
}

// printTasks writes one row per task. NOTE is what a running task is doing,
// why a waiting task is blocked, a failed task's error, or a done task's
// answer, on one line: cut to fit, or with full whole.
func printTasks(w io.Writer, tasks []store.Summary, now time.Time, full bool) error {
	if len(tasks) == 0 {
		fmt.Fprintln(w, "no tasks")
		return nil
	}
	header := "TASK\tSTATUS\tMODEL\tSTEPS\tMODEL CALLS\tTOOL CALLS\tTOOL ERRORS\tTOKENS IN/OUT\tCOST\tUPDATED\tNOTE"
	var rows []row
	for _, t := range tasks {
		note := t.Current
		switch t.Status {
		case runner.TaskDone:
			note = t.Result
		case runner.TaskFailed, runner.TaskWaiting:
			note = t.LastError
		}
		if full {
			note = squeeze(note)
		} else {
			note = oneLine(note, 50)
		}
		rows = append(rows, row{cells: fmt.Sprintf("%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d/%d\t$%.4f\t%s ago\t%s",
			t.ID, t.Status, t.Model, t.Steps, t.ModelCalls, t.ToolCalls, t.ToolErrors,
			t.TokensIn, t.TokensOut, t.CostUSD, ago(now.Sub(t.Updated)), note)})
	}
	return printTable(w, header, rows)
}

// ago rounds d for display: 4s, 3m, 2h, 5d.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
