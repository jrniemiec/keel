package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jrniemiec/keel/internal/klog"
)

// growModel asks for a tool in every reply, in turn from tools. Its
// PromptTokens is the estimate of what it was sent, as a provider counts it.
// Reply number stopAt stops with stopReason instead.
type growModel struct {
	tools      []string
	stopAt     int
	stopReason string
	calls      int
	sent       [][]Message // what each call was sent
}

func (m *growModel) Ask(ctx context.Context, model string, msgs []Message, tools []ToolDef) (Message, error) {
	m.calls++
	m.sent = append(m.sent, slices.Clone(msgs))
	n := 0
	for _, msg := range msgs {
		n += approxMsgTokens(msg)
	}
	if m.calls == m.stopAt {
		return Message{Role: "assistant", Text: "partial", Stop: m.stopReason, PromptTokens: n, USD: 0.01}, nil
	}
	name := m.tools[(m.calls-1)%len(m.tools)]
	c := ToolCall{ID: fmt.Sprintf("c%d", m.calls), Name: name, Args: json.RawMessage(`{}`)}
	return Message{Role: "assistant", ToolCalls: []ToolCall{c}, Stop: StopToolUse, PromptTokens: n}, nil
}

// sizeTools returns readSize bytes from fs__read_file, a short text from
// the others.
type sizeTools struct{ readSize int }

var contextTools = []ToolDef{
	{Name: "fs__read_file", Hints: Hints{ReadOnly: true}, ServerHints: Hints{ReadOnly: true}},
	{Name: "fs__write_file"},
}

func (s sizeTools) List(ctx context.Context) ([]ToolDef, error) { return contextTools, nil }
func (s sizeTools) Call(ctx context.Context, call ToolCall) (string, bool, error) {
	if call.Name == "fs__read_file" {
		return strings.Repeat("line\n", s.readSize/5), false, nil
	}
	return "written", false, nil
}

// window10K is a model with a 10K-token context window.
func window10K(string) int { return 10_000 }

func newContextTask(maxSteps int) Task {
	task := newTask("fs__*")
	task.MaxSteps = maxSteps
	task.ClearToolResultsAtPercent = 70
	return task
}

// sentTokens estimates the size of msgs.
func sentTokens(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		n += approxMsgTokens(m)
	}
	return n
}

// clearedIDs returns the call IDs of the cleared results in msgs.
func clearedIDs(msgs []Message) []string {
	var ids []string
	for _, m := range msgs {
		if m.Role == "tool" && m.Text == clearedText {
			ids = append(ids, m.ToolCallID)
		}
	}
	return ids
}

// Reads of ~1K tokens each fill a 10K window: at 70% the oldest results are
// cleared, down to about 50%; the newest 5 are never cleared. The store
// keeps every result whole.
func TestRunClearsOldResults(t *testing.T) {
	store := &memStore{}
	model := &growModel{tools: []string{"fs__read_file"}}
	var progress strings.Builder
	var logged bytes.Buffer
	r := &Runner{Model: model, Tools: sizeTools{readSize: 4000}, Store: store, ContextTokens: window10K,
		Progress: &progress, Logger: klog.New(&logged, klog.LevelInfo)}

	if _, err := r.Run(context.Background(), newContextTask(20)); !errors.Is(err, ErrLimitReached) {
		t.Fatalf("err = %v, want ErrLimitReached (max steps)", err)
	}
	for i, msgs := range model.sent {
		if n := sentTokens(msgs); n > 7000 {
			t.Errorf("call %d: sent ~%d tokens, want at most 70%% of 10K", i+1, n)
		}
	}
	last := model.sent[len(model.sent)-1]
	var results []Message
	for _, m := range last {
		if m.Role == "tool" {
			results = append(results, m)
		}
	}
	for _, m := range results[len(results)-keepResults:] {
		if m.Text == clearedText {
			t.Errorf("result %s cleared, want the newest %d kept", m.ToolCallID, keepResults)
		}
	}
	if len(clearedIDs(last)) == 0 {
		t.Error("no result cleared")
	}
	if !slices.ContainsFunc(store.steps, func(s Step) bool { return s.Kind == ResultsCleared }) {
		t.Error("no ResultsCleared step saved")
	}
	for _, s := range store.steps {
		if s.Kind == ToolResult && s.Msg.Text == clearedText {
			t.Errorf("stored result %s cleared, want it whole", s.CallID)
		}
	}
	if !strings.Contains(logged.String(), klog.MsgResultsCleared) || !strings.Contains(progress.String(), "clear  ") {
		t.Errorf("clearing not logged or shown:\n%s\n%s", logged.String(), progress.String())
	}
	// Clearing happens in batches: most calls send the same cleared results
	// as the call before.
	same := 0
	for i := 1; i < len(model.sent); i++ {
		if slices.Equal(clearedIDs(model.sent[i]), clearedIDs(model.sent[i-1])) {
			same++
		}
	}
	if same < len(model.sent)/2 {
		t.Errorf("cleared results changed in %d of %d calls, want batches", len(model.sent)-1-same, len(model.sent)-1)
	}
}

// A write tool's result is never cleared: with its call it records what
// was done.
func TestRunKeepsWriteResults(t *testing.T) {
	model := &growModel{tools: []string{"fs__read_file", "fs__write_file"}}
	r := &Runner{Model: model, Tools: sizeTools{readSize: 4000}, Store: &memStore{}, ContextTokens: window10K,
		Logger: klog.New(&bytes.Buffer{}, klog.LevelInfo)}

	r.Run(context.Background(), newContextTask(30))
	names := callNames(model.sent[len(model.sent)-1])
	cleared := clearedIDs(model.sent[len(model.sent)-1])
	if len(cleared) == 0 {
		t.Fatal("no result cleared")
	}
	for _, id := range cleared {
		if names[id] != "fs__read_file" {
			t.Errorf("result of %s (%s) cleared, want only reads", id, names[id])
		}
	}
}

// A resumed task sends the results cleared before as cleared: the point
// reached is saved.
func TestRunClearedOnResume(t *testing.T) {
	store := &memStore{}
	first := &growModel{tools: []string{"fs__read_file"}}
	r := &Runner{Model: first, Tools: sizeTools{readSize: 4000}, Store: store, ContextTokens: window10K,
		Logger: klog.New(&bytes.Buffer{}, klog.LevelInfo)}
	task := newContextTask(12)
	r.Run(context.Background(), task)
	before := clearedIDs(first.sent[len(first.sent)-1])
	if len(before) == 0 {
		t.Fatal("no result cleared in the first run")
	}

	second := &growModel{tools: []string{"fs__read_file"}}
	r.Model = second
	task.MaxSteps = 13
	r.Run(context.Background(), task)
	after := clearedIDs(second.sent[0])
	for _, id := range before {
		if !slices.Contains(after, id) {
			t.Errorf("result %s cleared before the resume, sent whole after it", id)
		}
	}
}

// With an unknown window nothing is cleared, and keel warns.
func TestRunNoClearingWithoutWindow(t *testing.T) {
	model := &growModel{tools: []string{"fs__read_file"}}
	var logged bytes.Buffer
	r := &Runner{Model: model, Tools: sizeTools{readSize: 4000}, Store: &memStore{},
		Logger: klog.New(&logged, klog.LevelInfo)}

	r.Run(context.Background(), newContextTask(12))
	if ids := clearedIDs(model.sent[len(model.sent)-1]); len(ids) > 0 {
		t.Errorf("cleared %v, want none without a window", ids)
	}
	if !strings.Contains(logged.String(), klog.MsgNoContextTokens) {
		t.Errorf("no %q warning:\n%s", klog.MsgNoContextTokens, logged.String())
	}
}

// A result over MaxToolResultTokens is cut in what's sent, whole in the store.
func TestRunCapsToolResults(t *testing.T) {
	store := &memStore{}
	model := &growModel{tools: []string{"fs__read_file"}}
	var logged bytes.Buffer
	r := &Runner{Model: model, Tools: sizeTools{readSize: 4000}, Store: store, Logger: klog.New(&logged, klog.LevelInfo)}
	task := newContextTask(2)
	task.MaxToolResultTokens = 100

	r.Run(context.Background(), task)
	res := model.sent[1][2]
	if res.Role != "tool" || len(res.Text) > 400+60 || !strings.Contains(res.Text, "[cut: first ~100 of ~1K tokens; ask for a smaller part]") {
		t.Errorf("sent result = %d bytes %q…, want ~400 bytes and the cut note", len(res.Text), cut(res.Text, 40))
	}
	for _, s := range store.steps {
		if s.Kind == ToolResult && len(s.Msg.Text) != 4000 {
			t.Errorf("stored result = %d bytes, want 4000", len(s.Msg.Text))
		}
	}
	if !strings.Contains(logged.String(), klog.MsgToolResultCut) {
		t.Errorf("no %q in the log:\n%s", klog.MsgToolResultCut, logged.String())
	}
}

// A reply cut off at the end of the window is saved (it cost money) but not
// added to the conversation; the task waits, and a resume asks again.
func TestRunStopContextFull(t *testing.T) {
	store := &memStore{}
	model := &growModel{tools: []string{"fs__read_file"}, stopAt: 2, stopReason: StopContextFull}
	r := &Runner{Model: model, Tools: sizeTools{readSize: 40}, Store: store, Logger: klog.New(&bytes.Buffer{}, klog.LevelInfo)}

	res, err := r.Run(context.Background(), newContextTask(5))
	if !errors.Is(err, ErrContextFull) || !errors.Is(err, ErrLimitReached) {
		t.Fatalf("err = %v, want ErrContextFull", err)
	}
	if store.blocked == "" || store.finished != "" || res.USD != 0.01 {
		t.Errorf("blocked %q, finished %q, USD %v; want waiting, the reply's cost counted", store.blocked, store.finished, res.USD)
	}

	if !slices.ContainsFunc(store.steps, func(s Step) bool { return s.Kind == ModelReply && s.Msg.Stop == StopContextFull }) {
		t.Error("the cut-off reply isn't saved")
	}

	resumed := &growModel{tools: []string{"fs__read_file"}, stopAt: 1, stopReason: StopEnd}
	r.Model = resumed
	if _, err := r.Run(context.Background(), newContextTask(5)); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if store.finished != TaskDone {
		t.Errorf("finished %q, want done", store.finished)
	}
	if slices.ContainsFunc(resumed.sent[0], func(m Message) bool { return m.Text == "partial" }) {
		t.Error("the cut-off reply was sent on resume")
	}
}

// A reply with PromptTokens shows the history's size on the progress line,
// with its percent of a known window.
func TestRunProgressShowsContext(t *testing.T) {
	model := &growModel{tools: []string{"fs__read_file"}}
	var progress strings.Builder
	r := &Runner{Model: model, Tools: sizeTools{readSize: 4000}, Store: &memStore{}, ContextTokens: window10K,
		Progress: &progress, Logger: klog.New(&bytes.Buffer{}, klog.LevelInfo)}

	r.Run(context.Background(), newContextTask(3))
	if want := "model  m: fs__read_file  history 1K of 10K (10%)\n"; !strings.Contains(progress.String(), want) {
		t.Errorf("progress has no %q:\n%s", want, progress.String())
	}
}

func TestEndsUnknownStop(t *testing.T) {
	if done, err := ends(Message{Stop: "content_filter"}); !done || !errors.Is(err, ErrUnknownStop) {
		t.Errorf("unknown stop: done %v, err %v; want done with ErrUnknownStop", done, err)
	}
	if done, err := ends(Message{Stop: StopEnd}); !done || err != nil {
		t.Errorf("end: done %v, err %v; want done, no error", done, err)
	}
	if done, err := ends(Message{ToolCalls: []ToolCall{{ID: "c"}}}); done || err != nil {
		t.Errorf("no stop, calls: done %v, err %v; want not done", done, err)
	}
}

func TestCapText(t *testing.T) {
	if got := capText("abc", 0); got != "abc" {
		t.Errorf("no cap: %q", got)
	}
	if got := capText("abcd", 1); got != "abcd" {
		t.Errorf("fits: %q", got)
	}
	long := strings.Repeat("x", 30) + "\n" + strings.Repeat("y", 30)
	if got := capText(long, 10); got != strings.Repeat("x", 30)+"\n[cut: first ~8 of ~16 tokens; ask for a smaller part]" {
		t.Errorf("at a line end: %q", got)
	}
	// "é" is 2 bytes; cutting at byte 4 would split the second one.
	if got := capText("aéé"+strings.Repeat("z", 20), 1); !strings.HasPrefix(got, "aé\n[cut:") {
		t.Errorf("split character: %q", got)
	}
}

func TestFormatTokens(t *testing.T) {
	tests := map[int]string{840: "840", 1000: "1K", 1234: "1.2K", 9_960: "10K", 84_400: "84K", 1_000_000: "1M", 1_250_000: "1.2M"}
	for n, want := range tests {
		if got := formatTokens(n); got != want {
			t.Errorf("formatTokens(%d) = %q, want %q", n, got, want)
		}
	}
	percents := map[float64]string{0.0044: "0.4%", 0.05: "5%", 0.42: "42%", 1: "100%"}
	for f, want := range percents {
		if got := formatPercent(f); got != want {
			t.Errorf("formatPercent(%v) = %q, want %q", f, got, want)
		}
	}
}
