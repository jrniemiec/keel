package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/jrniemiec/keel/internal/klog"
)

// paidModel asks for a read in every reply, each costing usd.
type paidModel struct {
	usd   float64
	calls int
}

func (m *paidModel) Ask(ctx context.Context, model string, msgs []Message, tools []ToolDef) (Message, error) {
	m.calls++
	c := ToolCall{ID: fmt.Sprintf("c%d", m.calls), Name: "fs__read_file", Args: json.RawMessage(`{}`)}
	return Message{Role: "assistant", ToolCalls: []ToolCall{c}, Stop: StopToolUse, USD: m.usd}, nil
}

var readTools = []ToolDef{{Name: "fs__read_file", Hints: Hints{ReadOnly: true}, ServerHints: Hints{ReadOnly: true}}}

// A task that used up max_usd waits; raised, it continues where it stopped,
// counting what it spent before.
func TestRunMaxUSD(t *testing.T) {
	store := &memStore{}
	model := &paidModel{usd: 0.3}
	var progress strings.Builder
	var logged bytes.Buffer
	r := &Runner{Model: model, Tools: &countTools{listed: readTools}, Store: store,
		Progress: &progress, Logger: klog.New(&logged, klog.LevelInfo)}
	task := newTask("fs__read_file")
	task.MaxSteps, task.MaxUSD = 20, 1

	res, err := r.Run(context.Background(), task)
	if !errors.Is(err, ErrLimitReached) || !strings.Contains(err.Error(), "cost: $1.20 of $1.00") {
		t.Fatalf("err = %v, want ErrLimitReached with cost: $1.20 of $1.00", err)
	}
	if model.calls != 4 || math.Abs(res.USD-1.2) > 1e-9 {
		t.Errorf("model calls = %d, USD = %v; want 4 calls, $1.20 (one reply may go over)", model.calls, res.USD)
	}
	if store.finished != "" || !strings.Contains(store.blocked, "cost: $1.20 of $1.00") {
		t.Errorf("finished %q, blocked %q; want the task waiting, not finished", store.finished, store.blocked)
	}
	if want := "model  m: fs__read_file  $0.300 (total $0.900 of $1.00)\n"; !strings.Contains(progress.String(), want) {
		t.Errorf("progress has no %q:\n%s", want, progress.String())
	}
	if n := strings.Count(logged.String(), klog.MsgBudgetNear); n != 1 {
		t.Errorf("%q logged %d times, want once:\n%s", klog.MsgBudgetNear, n, logged.String())
	}

	task.MaxUSD = 2
	res, err = r.Run(context.Background(), task)
	if !errors.Is(err, ErrLimitReached) || !strings.Contains(err.Error(), "cost: $2.10 of $2.00") {
		t.Fatalf("raised: err = %v, want ErrLimitReached with cost: $2.10 of $2.00", err)
	}
	if model.calls != 7 || math.Abs(res.USD-2.1) > 1e-9 {
		t.Errorf("raised: model calls = %d, USD = %v; want 7 calls, $2.10", model.calls, res.USD)
	}
}

// Used-up steps make the task wait too, instead of failing it.
func TestRunMaxStepsWaits(t *testing.T) {
	store := &memStore{}
	model := &paidModel{}
	r := &Runner{Model: model, Tools: &countTools{listed: readTools}, Store: store, Logger: klog.New(&bytes.Buffer{}, klog.LevelInfo)}
	task := newTask("fs__read_file")
	task.MaxSteps = 2

	_, err := r.Run(context.Background(), task)
	if !errors.Is(err, ErrLimitReached) || !strings.Contains(err.Error(), "model calls: 2 of 2") {
		t.Fatalf("err = %v, want ErrLimitReached with model calls: 2 of 2", err)
	}
	if model.calls != 2 || store.finished != "" || store.blocked == "" {
		t.Errorf("model calls %d, finished %q, blocked %q; want 2 calls, the task waiting", model.calls, store.finished, store.blocked)
	}

	task.MaxSteps = 3
	if _, err := r.Run(context.Background(), task); !errors.Is(err, ErrLimitReached) || model.calls != 3 {
		t.Errorf("raised: err = %v, model calls %d; want one more call, then ErrLimitReached", err, model.calls)
	}
}
