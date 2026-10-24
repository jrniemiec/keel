package runner

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestToolClass(t *testing.T) {
	tests := []struct {
		hints Hints
		want  string
	}{
		{Hints{ReadOnly: true}, ClassRead},
		{Hints{ReadOnly: true, Destructive: true}, ClassRead},
		{Hints{}, ClassWrite},
		{Hints{Idempotent: true}, ClassWrite},
		{Hints{Destructive: true}, ClassDestructive},
	}
	for _, tt := range tests {
		if got := ToolClass(tt.hints); got != tt.want {
			t.Errorf("ToolClass(%+v) = %q, want %q", tt.hints, got, tt.want)
		}
	}
}

func TestRunChecksApproval(t *testing.T) {
	task := newTask("fs__*")
	task.Approval.Write = "maybe"
	r := &Runner{Model: &endModel{}, Tools: listTools{}, Store: &memStore{}}
	if _, err := r.Run(context.Background(), task); err == nil || !strings.Contains(err.Error(), `approval write "maybe"`) {
		t.Errorf("err = %v, want a bad approval value", err)
	}
}

var approvalTools = []ToolDef{
	{Name: "fs__read_file", Hints: Hints{ReadOnly: true}, ServerHints: Hints{ReadOnly: true}},
	{Name: "fs__write_file", Hints: Hints{Destructive: true}, ServerHints: Hints{Destructive: true}},
	{Name: "fs__create_directory", Hints: Hints{Idempotent: true}, ServerHints: Hints{Idempotent: true}},
}

// askTask asks before writes and destructive calls.
func askTask() Task {
	task := newTask("fs__*")
	task.Approval = Approval{Read: ApprovalAuto, Write: ApprovalAsk, Destructive: ApprovalAsk}
	return task
}

// replyModel is a Model that asks for calls in its first reply, then ends
// the task. It keeps the conversation it was last sent.
type replyModel struct {
	calls []ToolCall
	sent  []Message
}

func (m *replyModel) Ask(ctx context.Context, model string, msgs []Message, tools []ToolDef) (Message, error) {
	m.sent = msgs
	if len(msgs) == 1 {
		return Message{Role: "assistant", ToolCalls: m.calls, Stop: StopToolUse}, nil
	}
	return Message{Role: "assistant", Text: "done", Stop: StopEnd}, nil
}

func call(id, tool string) ToolCall {
	return ToolCall{ID: id, Name: tool, Args: json.RawMessage(`{"path":"a"}`)}
}

// fakeApprover answers with decisions in order and records what it was asked.
// With waiting set, it closes it and waits for ctx to be done instead.
type fakeApprover struct {
	decisions []Decision
	err       error
	waiting   chan struct{}
	asked     []ApprovalRequest
}

func (a *fakeApprover) Ask(ctx context.Context, req ApprovalRequest) (Decision, error) {
	a.asked = append(a.asked, req)
	if a.waiting != nil {
		close(a.waiting)
		<-ctx.Done()
		return Decision{}, ctx.Err()
	}
	if a.err != nil {
		return Decision{}, a.err
	}
	d := a.decisions[0]
	a.decisions = a.decisions[1:]
	return d, nil
}

// allowed returns the Msg.Text of the intents in steps: why each call ran.
func allowed(steps []Step) string {
	var out []string
	for _, s := range steps {
		if s.Kind == Intent {
			out = append(out, s.Msg.Text)
		}
	}
	return strings.Join(out, ", ")
}

// kinds returns the kinds of steps, in order.
func kinds(steps []Step) string {
	var out []string
	for _, s := range steps {
		out = append(out, string(s.Kind))
	}
	return strings.Join(out, " ")
}

func TestApprovalAutoDoesNotAsk(t *testing.T) {
	store := &memStore{}
	tools := &countTools{listed: approvalTools}
	approver := &fakeApprover{}
	task := askTask()
	task.Approval.Destructive = ApprovalAuto
	r := &Runner{Model: &replyModel{calls: []ToolCall{call("c1", "fs__read_file"), call("c2", "fs__write_file")}}, Tools: tools, Store: store, Approver: approver}

	if _, err := r.Run(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if tools.calls != 2 || len(approver.asked) != 0 {
		t.Errorf("calls %d, asked %d; want 2 calls, none asked", tools.calls, len(approver.asked))
	}
	if got := allowed(store.steps); got != "auto: read, auto: destructive" {
		t.Errorf("intents say %q, want the policy and class", got)
	}
}

func TestApprovalApproved(t *testing.T) {
	store := &memStore{}
	tools := &countTools{listed: approvalTools}
	approver := &fakeApprover{decisions: []Decision{{Approved: true}}}
	r := &Runner{Model: &replyModel{calls: []ToolCall{call("c1", "fs__write_file")}}, Tools: tools, Store: store, Approver: approver}

	if _, err := r.Run(context.Background(), askTask()); err != nil {
		t.Fatal(err)
	}
	want := "tool_snapshot model_reply approval_asked approved intent tool_result model_reply"
	if got := kinds(store.steps); got != want {
		t.Errorf("steps = %s\nwant %s", got, want)
	}
	if tools.calls != 1 || len(approver.asked) != 1 || approver.asked[0].Reason != ClassDestructive {
		t.Errorf("calls %d, asked %+v; want 1 call, asked once as destructive", tools.calls, approver.asked)
	}
	if got := allowed(store.steps); got != AllowedApproved {
		t.Errorf("intent says %q, want %q", got, AllowedApproved)
	}
}

func TestApprovalRejected(t *testing.T) {
	store := &memStore{}
	tools := &countTools{listed: approvalTools}
	model := &replyModel{calls: []ToolCall{call("c1", "fs__write_file")}}
	approver := &fakeApprover{decisions: []Decision{{Message: "write to b instead"}}}
	r := &Runner{Model: model, Tools: tools, Store: store, Approver: approver}

	if _, err := r.Run(context.Background(), askTask()); err != nil {
		t.Fatal(err)
	}
	want := "tool_snapshot model_reply approval_asked rejected tool_result model_reply"
	if got := kinds(store.steps); got != want {
		t.Errorf("steps = %s\nwant %s", got, want)
	}
	if tools.calls != 0 {
		t.Errorf("calls = %d, want 0", tools.calls)
	}
	res := model.sent[len(model.sent)-1]
	if !res.IsError || res.Text != "not run: rejected by the user: write to b instead" {
		t.Errorf("the model got %+v, want the rejection with the message", res)
	}
}

func TestApprovalWithoutApprover(t *testing.T) {
	store := &memStore{}
	tools := &countTools{listed: approvalTools}
	model := &replyModel{calls: []ToolCall{call("c1", "fs__write_file")}}
	r := &Runner{Model: model, Tools: tools, Store: store}

	_, err := r.Run(context.Background(), askTask())
	if !errors.Is(err, ErrApprovalNeeded) || !strings.Contains(store.blocked, "fs__write_file (call c1)") || store.finished != "" {
		t.Fatalf("err %v, blocked %q, finished %q; want waiting", err, store.blocked, store.finished)
	}
	saved := len(store.steps)
	if got := kinds(store.steps); got != "tool_snapshot model_reply approval_asked" {
		t.Errorf("steps = %s", got)
	}

	// Again without an Approver: nothing new is saved.
	if _, err := r.Run(context.Background(), askTask()); !errors.Is(err, ErrApprovalNeeded) || len(store.steps) != saved {
		t.Errorf("err %v, steps %d; want waiting, still %d steps", err, len(store.steps), saved)
	}

	// In a terminal: asked once more, without a second ask step.
	approver := &fakeApprover{decisions: []Decision{{Approved: true}}}
	r.Approver = approver
	if _, err := r.Run(context.Background(), askTask()); err != nil {
		t.Fatal(err)
	}
	want := "tool_snapshot model_reply approval_asked approved intent tool_result model_reply"
	if got := kinds(store.steps); got != want || tools.calls != 1 || len(approver.asked) != 1 {
		t.Errorf("steps = %s, calls %d, asked %d\nwant %s, 1 call, asked once", got, tools.calls, len(approver.asked), want)
	}
}

func TestApprovalApproverCantAnswer(t *testing.T) {
	store := &memStore{}
	approver := &fakeApprover{err: ErrApprovalNeeded}
	r := &Runner{Model: &replyModel{calls: []ToolCall{call("c1", "fs__write_file")}}, Tools: &countTools{listed: approvalTools}, Store: store, Approver: approver}

	if _, err := r.Run(context.Background(), askTask()); !errors.Is(err, ErrApprovalNeeded) || store.blocked == "" {
		t.Errorf("err %v, blocked %q; want waiting", err, store.blocked)
	}
}

func TestApprovalInterrupted(t *testing.T) {
	store := &memStore{}
	approver := &fakeApprover{waiting: make(chan struct{})}
	r := &Runner{Model: &replyModel{calls: []ToolCall{call("c1", "fs__write_file")}}, Tools: &countTools{listed: approvalTools}, Store: store, Approver: approver}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-approver.waiting // at the prompt
		cancel()
	}()
	if _, err := r.Run(ctx, askTask()); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want canceled", err)
	}
	if store.finished != "" || store.blocked != "" {
		t.Errorf("finished %q, blocked %q; want the task left running", store.finished, store.blocked)
	}
}

// history returns a task's steps after a reply asking for calls, with more
// steps after it.
func history(calls []ToolCall, more ...Step) []Step {
	steps := []Step{
		{Kind: ToolSnapshot, Tools: approvalTools},
		{Kind: ModelReply, Msg: Message{Role: "assistant", ToolCalls: calls, Stop: StopToolUse}},
	}
	return append(steps, more...)
}

func step(kind StepKind, c ToolCall, text string) Step {
	return Step{Kind: kind, CallID: c.ID, Msg: Message{Text: text, ToolCalls: []ToolCall{c}}}
}

func TestApprovalResume(t *testing.T) {
	c1 := call("c1", "fs__write_file")
	tests := []struct {
		name      string
		saved     []Step
		decisions []Decision
		wantAsked []string // reasons
		wantCalls int
		wantErr   error
		wantKinds string // the steps added by the run
		wantWhy   string // what the added intents say
	}{
		{
			name:      "approved before a restart: not asked again",
			saved:     []Step{step(ApprovalAsked, c1, ClassDestructive), step(Approved, c1, "")},
			wantCalls: 1,
			wantKinds: "intent tool_result model_reply",
			wantWhy:   AllowedApproved,
		},
		{
			name:      "rejected before a restart: the model is told",
			saved:     []Step{step(ApprovalAsked, c1, ClassDestructive), step(Rejected, c1, "no")},
			wantKinds: "tool_result model_reply",
		},
		{
			name:      "approval used by an intent: outcome unknown is asked",
			saved:     []Step{step(ApprovalAsked, c1, ClassDestructive), step(Approved, c1, ""), step(Intent, c1, "")},
			wantErr:   ErrApprovalNeeded,
			wantKinds: "approval_asked",
		},
		{
			name:      "outcome unknown approved: a new intent, run once",
			saved:     []Step{step(Intent, c1, "")},
			decisions: []Decision{{Approved: true, ForRun: true}},
			wantAsked: []string{ReasonOutcomeUnknown},
			wantCalls: 1,
			wantKinds: "approval_asked approved intent tool_result model_reply",
			wantWhy:   AllowedRerunApproved,
		},
		{
			name:      "outcome unknown rejected",
			saved:     []Step{step(Intent, c1, "")},
			decisions: []Decision{{}},
			wantAsked: []string{ReasonOutcomeUnknown},
			wantKinds: "approval_asked rejected tool_result model_reply",
		},
		{
			name:      "outcome unknown approved, then died again: asked again",
			saved:     []Step{step(Intent, c1, ""), step(ApprovalAsked, c1, ReasonOutcomeUnknown), step(Approved, c1, ""), step(Intent, c1, "")},
			wantErr:   ErrApprovalNeeded,
			wantKinds: "approval_asked",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memStore{steps: history([]ToolCall{c1}, tt.saved...)}
			saved := len(store.steps)
			tools := &countTools{listed: approvalTools}
			model := &replyModel{}
			r := &Runner{Model: model, Tools: tools, Store: store}
			var approver *fakeApprover
			if tt.decisions != nil {
				approver = &fakeApprover{decisions: tt.decisions}
				r.Approver = approver
			}

			_, err := r.Run(context.Background(), askTask())
			if tt.wantErr == nil && err != nil || tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if got := kinds(store.steps[saved:]); got != tt.wantKinds {
				t.Errorf("added steps = %s, want %s", got, tt.wantKinds)
			}
			if tools.calls != tt.wantCalls {
				t.Errorf("calls = %d, want %d", tools.calls, tt.wantCalls)
			}
			if got := allowed(store.steps[saved:]); got != tt.wantWhy {
				t.Errorf("added intents say %q, want %q", got, tt.wantWhy)
			}
			if approver != nil {
				var reasons []string
				for _, req := range approver.asked {
					reasons = append(reasons, req.Reason)
				}
				if strings.Join(reasons, ",") != strings.Join(tt.wantAsked, ",") {
					t.Errorf("asked %v, want %v", reasons, tt.wantAsked)
				}
			}
			if tt.name == "rejected before a restart: the model is told" {
				if res := model.sent[len(model.sent)-1]; res.Text != "not run: rejected by the user: no" {
					t.Errorf("the model got %q", res.Text)
				}
			}
		})
	}
}

func TestApprovalForRun(t *testing.T) {
	store := &memStore{}
	tools := &countTools{listed: approvalTools}
	calls := []ToolCall{call("c1", "fs__write_file"), call("c2", "fs__create_directory"), call("c3", "fs__write_file")}
	approver := &fakeApprover{decisions: []Decision{{Approved: true, ForRun: true}, {Approved: true}}}
	r := &Runner{Model: &replyModel{calls: calls}, Tools: tools, Store: store, Approver: approver}

	if _, err := r.Run(context.Background(), askTask()); err != nil {
		t.Fatal(err)
	}
	if len(approver.asked) != 2 || approver.asked[1].Call.ID != "c2" {
		t.Errorf("asked %d times, want c1 and c2 (another tool), not c3", len(approver.asked))
	}
	if tools.calls != 3 {
		t.Errorf("calls = %d, want 3", tools.calls)
	}
	var c3 []string
	for _, s := range store.steps {
		if s.CallID == "c3" {
			c3 = append(c3, string(s.Kind)+":"+s.Msg.Text)
		}
	}
	if got := strings.Join(c3, " "); got != "approved:approved for this run intent:approved tool_result:ok" {
		t.Errorf("c3 steps = %s, want approved for this run, no ask", got)
	}

	// A resume doesn't remember it: c3 of a new reply asks again.
	store = &memStore{steps: history([]ToolCall{calls[0], calls[2]},
		step(ApprovalAsked, calls[0], ClassDestructive), step(Approved, calls[0], forRunText),
		step(Intent, calls[0], ""), Step{Kind: ToolResult, CallID: "c1", Msg: Message{Role: "tool", ToolCallID: "c1", Text: "ok"}})}
	r = &Runner{Model: &replyModel{}, Tools: &countTools{listed: approvalTools}, Store: store}
	if _, err := r.Run(context.Background(), askTask()); !errors.Is(err, ErrApprovalNeeded) || !strings.Contains(err.Error(), "call c3") {
		t.Errorf("err = %v, want c3 waiting for approval", err)
	}
}

func TestRunProgress(t *testing.T) {
	var out strings.Builder
	calls := []ToolCall{call("c1", "fs__read_file"), call("c2", "fs__write_file")}
	approver := &fakeApprover{decisions: []Decision{{}}}
	r := &Runner{Model: &replyModel{calls: calls}, Tools: &countTools{listed: approvalTools}, Store: &memStore{}, Approver: approver, Progress: &out}

	if _, err := r.Run(context.Background(), askTask()); err != nil {
		t.Fatal(err)
	}
	want := `model  m: fs__read_file, fs__write_file
tool   fs__read_file {"path":"a"}: ok
tool   fs__write_file {"path":"a"}: error: not run: rejected by the user
model  m: end
`
	if out.String() != want {
		t.Errorf("progress:\n%s\nwant:\n%s", out.String(), want)
	}
}
