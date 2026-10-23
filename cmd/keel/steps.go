package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/jrniemiec/keel/runner"
	"github.com/jrniemiec/keel/store"
)

// steps is "keel steps": it prints one task's steps in order.
func steps(args []string, w io.Writer) error {
	flags, f := newShowFlags("steps", false)
	rest, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	if f.help {
		fmt.Fprint(w, helpText)
		return nil
	}
	switch len(rest) {
	case 0:
		return usageError{"no task ID given"}
	case 1:
	default:
		return usageError{fmt.Sprintf("one task ID at a time, got %d", len(rest))}
	}
	taskID := rest[0]

	db, err := openDB(f.config, f.db)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	history, err := db.Load(ctx, taskID)
	if err != nil {
		return err
	}
	if len(history) == 0 {
		all, err := db.Tasks(ctx)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(all, func(t store.Summary) bool { return t.ID == taskID }) {
			return fmt.Errorf("no task %q; keel tasks lists them", taskID)
		}
		fmt.Fprintln(w, "no steps")
		return nil
	}
	return printSteps(w, history, f.full)
}

// printSteps writes one row per step. Text is cut to one line, or with full
// printed whole below the row, with tool args.
func printSteps(w io.Writer, history []runner.Step, full bool) error {
	const width = 60
	toolNames := map[string]string{} // by call ID, from the intents
	var rows []row
	for i, s := range history {
		var detail, below string
		m := s.Msg
		switch s.Kind {
		case runner.ToolSnapshot:
			detail = fmt.Sprintf("%d tools", len(s.Tools))
			if full {
				var names []string
				for _, t := range s.Tools {
					names = append(names, t.Name)
				}
				below = strings.Join(names, "\n")
			}
		case runner.ModelReply:
			detail = strings.TrimSpace(m.Model + " " + m.Stop)
			var calls []string
			for _, c := range m.ToolCalls {
				calls = append(calls, c.Name)
			}
			switch {
			case len(calls) > 0:
				detail += ": " + strings.Join(calls, ", ")
			case m.Text != "" && !full:
				detail += `: "` + oneLine(m.Text, width) + `"`
			}
			if full {
				below = m.Text
			}
		case runner.Intent:
			if len(m.ToolCalls) > 0 {
				c := m.ToolCalls[0]
				toolNames[s.CallID] = c.Name
				detail = c.Name
				if full {
					below = string(c.Args)
				}
			}
		case runner.ToolResult:
			result := "ok"
			if m.IsError {
				result = "error"
			}
			detail = strings.TrimSpace(toolNames[s.CallID] + " " + result)
			if full {
				below = m.Text
			} else if m.Text != "" {
				detail += ` "` + oneLine(m.Text, width) + `"`
			}
		}
		rows = append(rows, row{cells: fmt.Sprintf("%d\t%s\t%s", i+1, s.Kind, detail), below: below})
	}
	return printTable(w, "SEQ\tKIND\tDETAIL", rows)
}
