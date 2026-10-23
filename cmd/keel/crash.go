package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jrniemiec/keel/internal/klog"
	"github.com/jrniemiec/keel/runner"
)

// For testing crash and resume: the hidden --crash-at flag.
var (
	crashAt int // exit before the Nth model or tool call; 0 = never
	calls   int // model and tool calls made by this process
)

// maybeCrash simulates a crash: the process exits before the Nth call runs.
func maybeCrash(what string) {
	calls++
	if calls == crashAt {
		fmt.Printf("crash before call %d (%s)\n", calls, what)
		klog.Warn(klog.MsgCrash, "call", calls, "what", what)
		os.Exit(1)
	}
}

// crashModel wraps a model so that --crash-at counts model calls.
type crashModel struct {
	runner.Model
}

func (m crashModel) Ask(ctx context.Context, model string, msgs []runner.Message, tools []runner.ToolDef) (runner.Message, error) {
	maybeCrash("model")
	return m.Model.Ask(ctx, model, msgs, tools)
}

// crashTools wraps the tools so that --crash-at counts tool calls.
type crashTools struct {
	runner.Tools
}

func (t crashTools) Call(ctx context.Context, call runner.ToolCall) (string, bool, error) {
	maybeCrash(call.Name)
	return t.Tools.Call(ctx, call)
}
