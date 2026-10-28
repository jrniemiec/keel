// Command kstat shows keel's tasks and their progress: status, steps,
// calls, tokens and what each task is doing. It only reads the database,
// so it can run in its own window next to a running keel; -w keeps it
// refreshing.
//
//	kstat -w
//	kstat -db /tmp/test.db
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jrniemiec/keel/config"
	"github.com/jrniemiec/keel/internal/version"
	"github.com/jrniemiec/keel/store"
)

var buildVersion = "dev" // set with -X by the Makefile and the release build

func main() {
	dbPath := flag.String("db", defaultDBPath(), "SQLite database file")
	watch := flag.Bool("w", false, "refresh every second until Ctrl-C")
	showVersion := flag.Bool("version", false, "print kstat's version")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), `kstat shows keel's tasks and their progress, latest first. It only
reads the database; run it in its own window next to keel.

Usage:
  kstat [-w] [-db file]
  kstat -version

Examples:
  kstat -w                            watch all tasks, refreshed every second
  kstat -db /tmp/test.db             tasks in another database

NOTE is what a running task is doing, a failed task's error, or a done
task's answer.

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("kstat", version.String(buildVersion))
		return
	}

	db, err := store.OpenSQLite(*dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	for {
		tasks, err := db.Tasks(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if *watch {
			fmt.Print("\033[H\033[2J") // clear the screen
			fmt.Printf("%s  %s  (Ctrl-C to stop)\n\n", time.Now().Format("15:04:05"), *dbPath)
		}
		printTasks(os.Stdout, tasks, time.Now())
		if !*watch {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// printTasks writes one line per task. NOTE is what a running task is doing,
// why a waiting task is blocked, a failed task's error, or a finished task's
// answer.
func printTasks(w io.Writer, tasks []store.Summary, now time.Time) {
	if len(tasks) == 0 {
		fmt.Fprintln(w, "no tasks")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TASK\tSTATUS\tMODEL\tSTEPS\tMODEL CALLS\tTOOL CALLS\tTOOL ERRORS\tTOKENS IN/OUT\tCOST\tUPDATED\tNOTE")
	for _, t := range tasks {
		note := t.Current
		switch t.Status {
		case "done":
			note = t.Result
		case "failed", "waiting":
			note = t.LastError
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d/%d\t$%.4f\t%s ago\t%s\n",
			t.ID, t.Status, t.Model, t.Steps, t.ModelCalls, t.ToolCalls, t.ToolErrors,
			t.TokensIn, t.TokensOut, t.CostUSD, ago(now.Sub(t.Updated)), oneLine(note, 50))
	}
	tw.Flush()
}

// defaultDBPath returns the db in the keel config, as keel run uses it.
func defaultDBPath() string {
	kc, err := config.LoadKeelConfig("")
	if err != nil {
		return filepath.Join(config.Home(), "data", "keel.db")
	}
	return kc.DB
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

// oneLine squeezes s onto one line of at most max bytes.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max-3] + "..."
	}
	return s
}
