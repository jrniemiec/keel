package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jrniemiec/keel/runner"
)

// printLog prints the task's saved steps, one per line.
func printLog(st runner.Store, taskID string) {
	steps, err := st.Load(context.Background(), taskID)
	check(err)
	if len(steps) == 0 {
		fmt.Printf("no steps for task %s\n", taskID)
		return
	}
	for i, s := range steps {
		detail := s.Msg.Text
		if s.Msg.IsError {
			detail = "error: " + detail
		}
		for _, c := range s.Msg.ToolCalls {
			detail += c.Name + "(" + c.ID + ") "
		}
		fmt.Printf("%2d  %-11s %-3s %s\n", i+1, s.Kind, s.CallID, oneLine(detail, 60))
	}
}

// oneLine squeezes s onto one line of at most max bytes.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if max < 0 {
		max = 0
	}
	if len(s) > max {
		if max < 3 {
			return s[:max] // no room for "..."
		}
		s = s[:max-3] + "..."
	}
	return s
}
