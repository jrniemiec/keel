package runner

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/jrniemiec/keel/internal/klog"
)

// ToolDef describes a tool to the model. Tools.List returns them (from the MCP
// server's tools/list); Run matches them with Task.AllowedTools and passes the
// rest to the model. How a tool is run stays inside Tools.
type ToolDef struct {
	Name        string          // unique; matched by AllowedTools patterns and ToolCall.Name
	Description string          // tells the model what the tool does and when to use it
	InputSchema json.RawMessage // JSON Schema for the arguments, e.g. {"type":"object","properties":{"query":{"type":"string"}}}
	Hints       Hints           // effective: the server's, then the config's overrides; kept in keel, not sent to the model
	ServerHints Hints           // as the server sent them; the tool snapshot compares these
}

// Hints describe a tool's effects (MCP tool annotations). They come from the
// tool's server and are untrusted; the keel config can override them. Tools
// sets the MCP defaults when a server omits a hint.
type Hints struct {
	ReadOnly    bool // changes nothing
	Destructive bool // may delete or overwrite; MCP default true
	Idempotent  bool // repeating with the same args has no further effect
	OpenWorld   bool // reaches outside systems (web, APIs); MCP default true
}

// ToolCall is the model's request to run a tool. All fields come from the
// model; Args must match the tool's InputSchema.
type ToolCall struct {
	ID   string // e.g. "toolu_01A…"
	Name string
	Args json.RawMessage // JSON object, e.g. {"query":"MCP","limit":5}; passed through undecoded
}

// Tools lists and runs the tools available to the model.
type Tools interface {
	List(ctx context.Context) ([]ToolDef, error)
	// Call runs a tool. isError means the tool ran and failed; out is the
	// error text and the model sees it. err means the tool couldn't be
	// called; the task fails, unless err wraps ErrServerDown.
	Call(ctx context.Context, tool ToolCall) (out string, isError bool, err error)
}

// ErrServerDown is wrapped by a Tools.Call error when the tool's server died:
// the call's outcome is unknown. The next Call to that server restarts it,
// once per run. Run retries the call only for a read-only or idempotent tool;
// otherwise the run stops and the task stays running.
var ErrServerDown = errors.New("tool server down")

// ErrOutcomeUnknown is returned by Run when a resumed task has an intent
// without a result for a tool that is neither read-only nor idempotent: the
// call may have run before the restart, so it isn't run again. The task
// stays as it was.
var ErrOutcomeUnknown = errors.New("tool call outcome unknown")

// Runner runs tasks: it drives the loop between the model and the tools.
type Runner struct {
	Model    Model
	Tools    Tools
	Store    Store
	Approver Approver     // asks before a call the task's Approval says "ask" for; nil: the task waits
	Progress io.Writer    // one line per model reply and tool call, for a human; nil = none
	Logger   *klog.Logger // progress log; nil = klog.Default()
}

// Task represents a task to be executed by the Runner. The Store saves it on
// the first run; a resume must bring the same settings (ErrTaskChanged),
// except Model and Thinking, which may change between runs.
type Task struct {
	ID           string
	Goal         string   // instructions for the model
	AllowedTools []string // tool names or path.Match patterns, e.g. "fs__read_*"
	Model        string   // e.g. "anthropic:claude-sonnet-5"
	MaxSteps     int
	Approval     Approval // per tool class: "auto" or "ask"

	// Not used by the runner: the Model is set up with them (one task per
	// process for now). Saved here so a resume checks them too.
	System   string // system prompt
	Thinking bool
}

// Model sends the conversation and tool definitions to an LLM and returns its reply.
type Model interface {
	Ask(ctx context.Context, model string, msgs []Message, tools []ToolDef) (Message, error)
}

// Message is one turn in the conversation: from the user, the model ("assistant"),
// or a tool result ("tool").
type Message struct {
	Role       string
	Text       string
	ToolCalls  []ToolCall
	ToolCallID string // set on Role "tool" messages
	IsError    bool   // Role "tool": the call failed or was not run; Text says why

	// Set on Role "assistant" messages by the Model.
	Stop      string          // why the model stopped: StopEnd, StopToolUse, …; "" = decide by ToolCalls
	Raw       json.RawMessage // the provider's original reply, sent back as is (keeps thinking blocks)
	RawFrom   string          // the provider that produced Raw, e.g. "anthropic"
	TokensIn  int             // input tokens the reply cost, retries included
	TokensOut int             // output tokens, thinking included
	Model     string          // the Task.Model that gave the reply; set by the Runner
}

// Stop reasons in Message.Stop.
const (
	StopEnd       = "end"        // final answer
	StopToolUse   = "tool_use"   // run ToolCalls
	StopMaxTokens = "max_tokens" // cut off at the output limit; ToolCalls may be incomplete
	StopRefusal   = "refusal"    // the model declined
)

// Errors that end a task because of the model's reply.
var (
	ErrCutOff  = errors.New("reply cut off at the output limit")
	ErrRefused = errors.New("model refused")
)

// ErrTaskChanged is returned by Store.StartTask when a resumed task's
// settings differ from those saved on its first run.
var ErrTaskChanged = errors.New("task settings differ from its first run")

// ErrToolsChanged is returned by Run when a resumed task's tools differ from
// its tool snapshot: a description, schema or hints changed, or none is left.
// The task is not started; it stays as it was.
var ErrToolsChanged = errors.New("tools changed since the task started")

// Result is the outcome of a finished task.
type Result struct {
	Text   string        // the model's final answer
	Steps  int           // model calls made
	Waited time.Duration // time this run spent waiting for approvals
}

// StepKind says what a saved step records.
type StepKind string

const (
	ModelReply StepKind = "model_reply" // the model's reply
	Intent     StepKind = "intent"      // about to run a tool call; Msg.Text says why it may (Allowed…)
	ToolResult StepKind = "tool_result" // the tool call's result

	ToolSnapshot StepKind = "tool_snapshot" // the task's allowed tools, saved once at start

	// Approvals; each has CallID and the call in Msg.ToolCalls. A decision
	// applies to the last ask of its call, until the call's next intent.
	ApprovalAsked StepKind = "approval_asked" // a call waits for a decision; Msg.Text is the reason
	Approved      StepKind = "approved"       // Msg.Text is "approved for this run" for an "always" answer
	Rejected      StepKind = "rejected"       // Msg.Text is the user's message for the model
)

// Step is one entry in a task's append-only log.
type Step struct {
	Kind   StepKind
	CallID string    // tool call ID; empty for model replies
	Msg    Message   // the reply or tool result; for an intent, ToolCalls holds the call
	Tools  []ToolDef // ToolSnapshot only
}

// Task statuses, kept by the Store with each task.
const (
	TaskRunning = "running"
	TaskWaiting = "waiting" // needs a human: blocked or waiting for approval (see Store.BlockTask)
	TaskDone    = "done"
	TaskFailed  = "failed"
)

// Store persists a task's steps so Run can resume after a crash, and a
// summary of each task (status, counters, what it's doing) for keel status.
type Store interface {
	// StartTask records task as running: a new task, or one resumed. A
	// resumed task must have the saved settings, else ErrTaskChanged.
	StartTask(ctx context.Context, task Task) error
	// Append saves s and updates the task's summary in one transaction;
	// durable when it returns.
	Append(ctx context.Context, taskID string, s Step) error
	Load(ctx context.Context, taskID string) ([]Step, error) // in append order
	// SetCurrent records what the task is doing, e.g. "asking model".
	SetCurrent(ctx context.Context, taskID, current string) error
	// FinishTask records the end of a task: status TaskDone with its result,
	// or TaskFailed with errText.
	FinishTask(ctx context.Context, taskID, status, result, errText string) error
	// BlockTask records a task stopped by its environment: status
	// TaskWaiting, with reason. StartTask sets it running again. A task not
	// in the store is left out.
	BlockTask(ctx context.Context, taskID, reason string) error
}

// Run executes task as a loop, one model call per step:
//
//  1. Ask: send the conversation so far plus the allowed tools to the model.
//  2. Record: add the model's reply to the conversation.
//  3. Check if done: if the reply has no tool calls, it's the final answer, so stop.
//     A reply cut off at the output limit or a refusal ends the task with an error.
//  4. Act: run each tool call the model asked for.
//  5. Observe: add each tool's result, or its error, to the conversation.
//  6. Repeat: loop back to 1, with the model now seeing those results.
//
// Every reply, intent and tool result is appended to r.Store first, so a
// restarted Run resumes from the log instead of starting over.
// It stops when the model is done or task.MaxSteps is reached.
//
// The task's status in r.Store ends as done or failed. If ctx is cancelled
// (shutdown), it stays running, so the task is resumed later.
func (r *Runner) Run(ctx context.Context, task Task) (Result, error) {
	began := time.Now()
	log := r.Logger
	if log == nil {
		log = klog.Default()
	}
	log = log.With("task", task.ID)

	if err := r.check(task); err != nil {
		return r.end(ctx, log, task, false, began, Result{}, err)
	}
	history, tools, err := r.prepare(ctx, task, log)
	if err != nil {
		return r.end(ctx, log, task, false, began, Result{}, err)
	}
	if err := r.Store.StartTask(ctx, task); err != nil {
		return r.end(ctx, log, task, false, began, Result{}, fmt.Errorf("save task: %w", err))
	}

	t, err := r.start(ctx, task, history, tools, log)
	var res Result
	if err == nil {
		res.Text, err = t.run(ctx)
	}
	if t != nil {
		res.Steps, res.Waited = t.steps, t.waited
	}
	return r.end(ctx, log, task, true, began, res, err)
}

// end records how a run ended, in the log and in the store, and returns res
// and err with the task's ID added:
//   - done: FinishTask(TaskDone);
//   - stopped (ctx done): no change, the task stays running;
//   - waiting for approval (ErrApprovalNeeded): BlockTask, status TaskWaiting;
//   - blocked by its environment (see blocked): BlockTask, status TaskWaiting;
//   - failed: FinishTask(TaskFailed) once the task is saved (started);
//     before that nothing is saved.
func (r *Runner) end(ctx context.Context, log *klog.Logger, task Task, started bool, began time.Time, res Result, err error) (Result, error) {
	attrs := []any{"model_calls", res.Steps, "dur", since(began)}
	if res.Waited > 0 {
		attrs = append(attrs, "waited", res.Waited.Round(time.Second))
	}
	var saveErr error
	switch {
	case err == nil:
		log.Info(klog.MsgTaskDone, attrs...)
		saveErr = r.Store.FinishTask(ctx, task.ID, TaskDone, res.Text, "")
	case ctx.Err() != nil:
		log.Warn(klog.MsgTaskStopped, append(attrs, "err", err)...)
	case errors.Is(err, ErrApprovalNeeded):
		log.Info(klog.MsgTaskWaiting, append(attrs, "reason", err)...)
		saveErr = r.Store.BlockTask(ctx, task.ID, err.Error())
	case blocked(err):
		log.Error(klog.MsgTaskBlocked, append(attrs, "err", err)...)
		saveErr = r.Store.BlockTask(ctx, task.ID, err.Error())
	default:
		log.Error(klog.MsgTaskFailed, append(attrs, "err", err)...)
		if started {
			saveErr = r.Store.FinishTask(ctx, task.ID, TaskFailed, "", err.Error())
		}
	}
	if saveErr != nil {
		err = errors.Join(err, fmt.Errorf("save status: %w", saveErr))
	}
	if err != nil {
		return Result{Steps: res.Steps, Waited: res.Waited}, fmt.Errorf("task %s: %w", task.ID, err)
	}
	return res, nil
}

// blocked reports whether err stops a task because of its environment, not
// the task: it needs a human, and the task waits instead of failing.
func blocked(err error) bool {
	return errors.Is(err, ErrToolsChanged) || errors.Is(err, ErrServerDown) || errors.Is(err, ErrOutcomeUnknown)
}

// taskRun is the state of one Run.
type taskRun struct {
	r       *Runner
	task    Task
	tools   []ToolDef         // the task's allowed tools
	msgs    []Message         // the conversation so far
	done    map[string]bool   // IDs of tool calls with a saved result
	pending map[string]bool   // IDs of tool calls with an intent and no result (from before a restart)
	asked   map[string]string // reasons of calls asked for approval since their last intent, by call ID
	decided map[string]Step   // approved or rejected steps since the call's last intent, by call ID
	// tools approved for the rest of this run ("always"); not saved, so a resume asks again
	approvedForRun map[string]bool
	waited         time.Duration // spent waiting for approvals
	steps          int           // model replies so far
	last           *Message      // the last saved reply when Run started; nil for a new task
	seq            int           // steps in the task's log so far; the next one is seq+1
	log            *klog.Logger  // with task=<id>
}

// check checks the runner and the task before anything is saved.
func (r *Runner) check(task Task) error {
	if r.Model == nil || r.Tools == nil || r.Store == nil {
		return errors.New("runner needs Model, Tools and Store")
	}
	if task.MaxSteps <= 0 {
		return errors.New("max steps must be > 0")
	}
	if task.Model == "" {
		return errors.New("model must be specified")
	}
	return task.Approval.Check()
}

// prepare loads the task's history and decides its tools, before anything is
// saved: errors here leave the task as it was. A task without a tool snapshot
// gets the tools its patterns match; a resumed one gets its snapshot's tools.
func (r *Runner) prepare(ctx context.Context, task Task, log *klog.Logger) ([]Step, []ToolDef, error) {
	listed, err := r.Tools.List(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list tools: %w", err)
	}
	history, err := r.Store.Load(ctx, task.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("load: %w", err)
	}
	snapshot := lastToolSnapshot(history)
	var tools []ToolDef
	switch {
	case snapshot == nil:
		tools, err = allowedTools(listed, task.AllowedTools)
	case finished(history):
		tools = snapshot // no tool runs; keel run prints the saved answer
	default:
		tools, err = toolsSinceSnapshot(snapshot, listed, task.AllowedTools, log)
	}
	if err != nil {
		return nil, nil, err
	}
	return history, tools, nil
}

// allowedTools returns the listed tools that patterns match. A pattern that
// matches no tool is an error, so a typo stops the task before it is saved.
func allowedTools(listed []ToolDef, patterns []string) ([]ToolDef, error) {
	allowed, unmatchedPatterns := matchTools(listed, patterns)
	if len(unmatchedPatterns) > 0 {
		return nil, fmt.Errorf("allow %q: no tool matches", unmatchedPatterns)
	}
	if len(allowed) == 0 {
		return nil, errors.New("no allowed tools available")
	}
	return allowed, nil
}

// toolsSinceSnapshot returns the snapshot's tools that are still listed, with
// the snapshot's definitions and the current effective Hints (overrides may
// change between runs). A tool whose description, schema or server hints
// changed is ErrToolsChanged, and so is no tool left. Tools that patterns
// match but the snapshot doesn't have are logged and ignored.
func toolsSinceSnapshot(snapshot, listed []ToolDef, patterns []string, log *klog.Logger) ([]ToolDef, error) {
	matched, _ := matchTools(listed, patterns)
	for _, tool := range matched {
		if !slices.ContainsFunc(snapshot, func(d ToolDef) bool { return d.Name == tool.Name }) {
			log.Info(klog.MsgToolAdded, "tool", tool.Name)
		}
	}
	var stillListed []ToolDef
	var changes []string // e.g. "fs__read_file: description, schema"
	for _, saved := range snapshot {
		if saved.ServerHints == (Hints{}) {
			saved.ServerHints = saved.Hints // saved before ServerHints, when there were no overrides
		}
		i := slices.IndexFunc(listed, func(d ToolDef) bool { return d.Name == saved.Name })
		if i < 0 {
			log.Warn(klog.MsgToolRemoved, "tool", saved.Name)
			continue
		}
		if fields := changedFields(saved, listed[i]); len(fields) > 0 {
			changes = append(changes, saved.Name+": "+strings.Join(fields, ", "))
			continue
		}
		saved.Hints = listed[i].Hints // the current overrides apply
		stillListed = append(stillListed, saved)
	}
	if len(changes) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrToolsChanged, strings.Join(changes, "; "))
	}
	if len(stillListed) == 0 {
		return nil, fmt.Errorf("%w: no tool of the snapshot is listed", ErrToolsChanged)
	}
	return stillListed, nil
}

// changedFields names the fields of listed that differ from saved:
// "description", "schema", "hints".
func changedFields(saved, listed ToolDef) []string {
	var fields []string
	if saved.Description != listed.Description {
		fields = append(fields, "description")
	}
	if !sameJSON(saved.InputSchema, listed.InputSchema) {
		fields = append(fields, "schema")
	}
	if saved.ServerHints != listed.ServerHints { // overrides may change; the server's hints may not
		fields = append(fields, "hints")
	}
	return fields
}

// sameJSON reports whether a and b hold the same JSON value; key order is
// ignored. Invalid JSON is compared byte by byte.
func sameJSON(a, b json.RawMessage) bool {
	var va, vb any
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(va, vb)
}

// lastToolSnapshot returns the tools of the last ToolSnapshot step in
// history, or nil if there is none.
func lastToolSnapshot(history []Step) []ToolDef {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Kind == ToolSnapshot {
			return history[i].Tools
		}
	}
	return nil
}

// finished reports whether the last model reply in history ends the task.
func finished(history []Step) bool {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Kind == ModelReply {
			done, _ := ends(history[i].Msg)
			return done
		}
	}
	return false
}

// start rebuilds the task's state from history and saves a tool snapshot
// if the task has none; tools are the task's allowed tools.
func (r *Runner) start(ctx context.Context, task Task, history []Step, tools []ToolDef, log *klog.Logger) (*taskRun, error) {
	t := &taskRun{r: r, task: task, tools: tools, log: log}
	t.replay(history)
	if lastToolSnapshot(history) == nil {
		if err := t.save(ctx, Step{Kind: ToolSnapshot, Tools: tools}); err != nil {
			return nil, fmt.Errorf("save tool snapshot: %w", err)
		}
		if len(history) > 0 {
			log.Warn(klog.MsgToolSnapshot, "reason", "task saved before tool snapshots")
		}
	}
	if len(history) == 0 {
		log.Info(klog.MsgTaskStart, "model", task.Model, "max_steps", task.MaxSteps, "tools", len(tools))
	} else {
		log.Info(klog.MsgTaskResume, "model", task.Model, "steps", t.seq, "model_calls", t.steps)
		if t.last != nil && t.last.Model != "" && t.last.Model != task.Model {
			log.Info(klog.MsgModelChanged, "from", t.last.Model, "to", task.Model)
		}
	}
	return t, nil
}

// run is the loop of Run. It returns the model's final answer.
func (t *taskRun) run(ctx context.Context) (string, error) {
	// Resume: finish the last saved reply first.
	if t.last != nil {
		if done, err := ends(*t.last); done { // finished before the restart
			if err != nil {
				return "", err
			}
			return t.last.Text, nil
		}
		if err := t.act(ctx, *t.last); err != nil {
			return "", err
		}
	}

	for t.steps < t.task.MaxSteps {
		reply, err := t.ask(ctx) // 1. Ask, 2. Record
		if err != nil {
			return "", err
		}
		if done, err := ends(reply); done { // 3. Check if done
			if err != nil {
				return "", err
			}
			return reply.Text, nil
		}
		if err := t.act(ctx, reply); err != nil { // 4. Act, 5. Observe
			return "", err
		}
	}
	return "", fmt.Errorf("step limit %d reached", t.task.MaxSteps)
}

// ends reports whether reply ends the task, and the error if it ends it
// without an answer. A reply without Stop (e.g. from a scripted model) ends
// the task when it has no tool calls.
func ends(reply Message) (bool, error) {
	switch reply.Stop {
	case StopMaxTokens:
		// The Model already retried with a larger limit (local/max-tokens.md).
		// ToolCalls may be incomplete, so never run them.
		return true, ErrCutOff
	case StopRefusal:
		return true, fmt.Errorf("%w: %s", ErrRefused, reply.Text)
	}
	return len(reply.ToolCalls) == 0, nil
}

// ask gets the next reply from the model, saves it and adds it to the conversation.
func (t *taskRun) ask(ctx context.Context) (Message, error) {
	if err := t.r.Store.SetCurrent(ctx, t.task.ID, "asking "+t.task.Model); err != nil {
		return Message{}, fmt.Errorf("save status: %w", err)
	}
	began := time.Now()
	reply, err := t.r.Model.Ask(ctx, t.task.Model, t.msgs, t.tools)
	if err != nil {
		return Message{}, fmt.Errorf("step %d: ask model: %w", t.steps, err)
	}
	reply.Model = t.task.Model
	if err := t.save(ctx, Step{Kind: ModelReply, Msg: reply}); err != nil {
		return Message{}, fmt.Errorf("save reply: %w", err)
	}
	t.log.Info(klog.MsgModelReply, "seq", t.seq, "kind", ModelReply, "model", t.task.Model,
		"stop", reply.Stop, "calls", len(reply.ToolCalls),
		"in", reply.TokensIn, "out", reply.TokensOut, "dur", since(began))
	t.log.Debug(klog.MsgModelText, "seq", t.seq, "text", reply.Text)
	var calls []string
	for _, c := range reply.ToolCalls {
		calls = append(calls, c.Name)
	}
	if len(calls) > 0 {
		t.progress("model  %s: %s", t.task.Model, strings.Join(calls, ", "))
	} else {
		t.progress("model  %s: %s", t.task.Model, cmp.Or(reply.Stop, "done"))
	}
	t.msgs = append(t.msgs, reply)
	t.steps++
	return reply, nil
}

// act runs each tool call in reply that has no result yet, saves the results
// and adds them to the conversation.
func (t *taskRun) act(ctx context.Context, reply Message) error {
	for _, call := range reply.ToolCalls {
		if t.done[call.ID] {
			t.log.Debug(klog.MsgToolSkipped, "tool", call.Name, "call_id", call.ID)
			continue // ran before the restart
		}
		began := time.Now()
		out, failed, err := t.callTool(ctx, call)
		if err != nil {
			return err
		}
		res := Message{Role: "tool", Text: out, ToolCallID: call.ID, IsError: failed}
		// The call has run, so save its result even if ctx was cancelled meanwhile;
		// otherwise resume would run it again.
		if err := t.save(context.WithoutCancel(ctx), Step{Kind: ToolResult, CallID: call.ID, Msg: res}); err != nil {
			return fmt.Errorf("save result: %w", err)
		}
		attrs := []any{"seq", t.seq, "kind", ToolResult, "tool", call.Name, "call_id", call.ID, "dur", since(began)}
		if failed {
			t.log.Warn(klog.MsgToolResult, append(attrs, "failed", true, "err", out)...)
			t.progress("tool   %s %s: error: %s", call.Name, cut(string(call.Args), 80), cut(out, 80))
		} else {
			t.log.Info(klog.MsgToolResult, attrs...)
			t.log.Debug(klog.MsgToolOut, "seq", t.seq, "text", out)
			t.progress("tool   %s %s: ok", call.Name, cut(string(call.Args), 80))
		}
		t.msgs = append(t.msgs, res)
		t.done[call.ID] = true
	}
	return nil
}

// callTool runs call and returns the text for its tool result. A call to a tool
// the task doesn't allow is not run, a rejected call is not run, and a tool
// that ran and failed returns its error text, all with failed set; the model
// sees them and can adjust. A call that needs approval is decided first (see
// decide). It returns an error when a step can't be saved, ctx is done, no
// decision can be had or the tool can't be called. Then no result is saved,
// and resume handles the call again.
func (t *taskRun) callTool(ctx context.Context, call ToolCall) (out string, failed bool, err error) {
	tool, ok := t.tool(call.Name)
	if !ok {
		t.log.Warn(klog.MsgToolDenied, "tool", call.Name, "call_id", call.ID)
		return fmt.Sprintf("tool %s is not available", call.Name), true, nil
	}
	decision, decided := t.decided[call.ID]
	approved := decided && decision.Kind == Approved
	if decided && decision.Kind == Rejected { // decided before a restart
		return rejectedText(t.asked[call.ID], decision.Msg.Text), true, nil
	}
	if reason := t.approvalReason(call, tool); !decided && reason != "" {
		d, err := t.decide(ctx, call, tool, reason)
		if err != nil {
			return "", false, err
		}
		if !d.Approved {
			return rejectedText(reason, d.Message), true, nil
		}
		approved = true
	}
	switch {
	case t.pending[call.ID] && !t.safeToRepeat(call.Name) && approved:
		// The user approved repeating a call that may have run. A new intent
		// uses the approval up: if this run dies too, the next one asks again.
		if err := t.saveIntent(ctx, call, AllowedRerunApproved); err != nil {
			return "", false, err
		}
		t.log.Warn(klog.MsgIntentRerun, "seq", t.seq, "tool", call.Name, "call_id", call.ID, "allowed", AllowedRerunApproved)
		delete(t.pending, call.ID)
	case t.pending[call.ID]:
		// An intent without a result: the call may have run before the
		// restart. Repeat it only if that's safe; its intent is saved already.
		if !t.safeToRepeat(call.Name) {
			return "", false, fmt.Errorf("%w: %s (call %s) may have run before the restart", ErrOutcomeUnknown, call.Name, call.ID)
		}
		t.log.Warn(klog.MsgIntentRerun, "tool", call.Name, "call_id", call.ID, "allowed", AllowedRerunSafe)
		delete(t.pending, call.ID)
	default:
		// Intent before the side effect, with why the call may run.
		allowed := AllowedAuto + ToolClass(tool.Hints)
		if approved {
			allowed = AllowedApproved
		}
		if err := t.saveIntent(ctx, call, allowed); err != nil {
			return "", false, err
		}
		t.log.Info(klog.MsgToolCall, "seq", t.seq, "kind", Intent, "tool", call.Name, "call_id", call.ID, "allowed", allowed)
	}
	t.log.Debug(klog.MsgToolArgs, "seq", t.seq, "args", string(call.Args))
	out, failed, err = t.r.Tools.Call(ctx, call)
	if errors.Is(err, ErrServerDown) && ctx.Err() == nil && t.safeToRepeat(call.Name) {
		t.log.Warn(klog.MsgToolRetry, "tool", call.Name, "call_id", call.ID, "err", err)
		out, failed, err = t.r.Tools.Call(ctx, call) // restarts the server
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", false, fmt.Errorf("call %s interrupted: %w", call.Name, ctx.Err())
		}
		return "", false, fmt.Errorf("call %s: %w", call.Name, err)
	}
	return out, failed, nil
}

// saveIntent saves an intent step for call; allowed says why it may run.
func (t *taskRun) saveIntent(ctx context.Context, call ToolCall, allowed string) error {
	if err := t.save(ctx, Step{Kind: Intent, CallID: call.ID, Msg: Message{Text: allowed, ToolCalls: []ToolCall{call}}}); err != nil {
		return fmt.Errorf("save intent: %w", err)
	}
	return nil
}

// safeToRepeat reports whether the task's tool name is read-only or
// idempotent, by the hints in the task's tools (its snapshot).
func (t *taskRun) safeToRepeat(name string) bool {
	i := slices.IndexFunc(t.tools, func(d ToolDef) bool { return d.Name == name })
	return i >= 0 && (t.tools[i].Hints.ReadOnly || t.tools[i].Hints.Idempotent)
}

// replay rebuilds the conversation from the steps saved by earlier runs.
// Approval steps don't go into the conversation; an intent uses up the
// call's ask and decision.
func (t *taskRun) replay(history []Step) {
	t.msgs = []Message{{Role: "user", Text: t.task.Goal}}
	t.done = map[string]bool{}
	t.pending = map[string]bool{}
	t.asked = map[string]string{}
	t.decided = map[string]Step{}
	t.approvedForRun = map[string]bool{}
	t.seq = len(history)
	for _, s := range history {
		switch s.Kind {
		case ModelReply:
			t.msgs = append(t.msgs, s.Msg)
			t.steps++
			m := s.Msg
			t.last = &m
		case Intent:
			t.pending[s.CallID] = true
			delete(t.asked, s.CallID)
			delete(t.decided, s.CallID)
		case ApprovalAsked:
			t.asked[s.CallID] = s.Msg.Text
			delete(t.decided, s.CallID)
		case Approved, Rejected:
			t.decided[s.CallID] = s
		case ToolResult:
			t.msgs = append(t.msgs, s.Msg)
			t.done[s.CallID] = true
			delete(t.pending, s.CallID)
		}
	}
}

// save appends s to the task's log and counts it in seq.
func (t *taskRun) save(ctx context.Context, s Step) error {
	if err := t.r.Store.Append(ctx, t.task.ID, s); err != nil {
		return err
	}
	t.seq++
	return nil
}

// progress writes one line to r.Progress, if set.
func (t *taskRun) progress(format string, args ...any) {
	if t.r.Progress != nil {
		fmt.Fprintf(t.r.Progress, format+"\n", args...)
	}
}

// cut returns s on one line, cut to at most n characters with "…".
func cut(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// since returns the time from began, rounded for the log.
func since(began time.Time) time.Duration {
	return time.Since(began).Round(time.Millisecond)
}

// matchTools returns the listed tools whose names match one of patterns
// (path.Match, e.g. "fs__read_*"), and the patterns that matched no tool.
func matchTools(listed []ToolDef, patterns []string) (matched []ToolDef, unmatchedPatterns []string) {
	patternMatched := make([]bool, len(patterns))
	for _, tool := range listed {
		toolMatched := false
		for i, pattern := range patterns {
			if ok, _ := path.Match(pattern, tool.Name); ok {
				patternMatched[i], toolMatched = true, true
			}
		}
		if toolMatched {
			matched = append(matched, tool)
		}
	}
	for i, pattern := range patterns {
		if !patternMatched[i] {
			unmatchedPatterns = append(unmatchedPatterns, pattern)
		}
	}
	return matched, unmatchedPatterns
}
