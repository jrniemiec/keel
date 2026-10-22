package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jrniemiec/keel/store"
)

// statusCmd runs "keel status": a table of tasks, refreshed every second
// with -w until Ctrl-C.
func statusCmd(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dbPath := fs.String("db", defaultPath("data", "keel.db"), "SQLite database file")
	watch := fs.Bool("w", false, "refresh every second until Ctrl-C")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: keel status [-w] [-db file]\n\nShows each task's status and summary, latest first.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

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
// a failed task's error, or a finished task's answer.
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
		case "failed":
			note = t.LastError
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d/%d\t$%.4f\t%s ago\t%s\n",
			t.ID, t.Status, t.Model, t.Steps, t.ModelCalls, t.ToolCalls, t.ToolErrors,
			t.TokensIn, t.TokensOut, t.CostUSD, ago(now.Sub(t.Updated)), oneLine(note, 50))
	}
	tw.Flush()
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
