package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/jrniemiec/keel/internal/klog"
	"github.com/jrniemiec/keel/runner"
)

var (
	crashAt = flag.Int("crash-at", 0, "exit before the Nth model or tool call (0 = never)")
	calls   int // model and tool calls made by this process
)

// maybeCrash simulates a crash: the process exits before the Nth call runs.
func maybeCrash(what string) {
	calls++
	if calls == *crashAt {
		fmt.Printf("crash before call %d (%s)\n", calls, what)
		klog.Warn(klog.MsgCrash, "call", calls, "what", what)
		os.Exit(1)
	}
}

// crashModel wraps a model so that -crash-at also counts model calls.
type crashModel struct {
	runner.Model
}

func (m crashModel) Ask(ctx context.Context, model string, msgs []runner.Message, tools []runner.ToolDef) (runner.Message, error) {
	maybeCrash("model")
	return m.Model.Ask(ctx, model, msgs, tools)
}
