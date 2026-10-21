package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// ToolDef describes a tool to the model. Tools.List returns them (from the MCP
// server's tools/list); Run filters them by Task.AllowedTools and passes the
// rest to the model. How a tool is run stays inside Tools.
type ToolDef struct {
	Name        string          // unique; matched by AllowedTools and ToolCall.Name
	Description string          // tells the model what the tool does and when to use it
	InputSchema json.RawMessage // JSON Schema for the arguments, e.g. {"type":"object","properties":{"query":{"type":"string"}}}
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
	Call(ctx context.Context, tool ToolCall) (string, error)
}

// Runner runs tasks: it drives the loop between the model and the tools.
type Runner struct {
	Model Model
	Tools Tools
	Store Store
}

// Task represents a task to be executed by the Runner.
type Task struct {
	ID           string
	Goal         string   // instructions for the model
	AllowedTools []string // tools by name
	Model        string   // e.g. "claude-sonnet-5"
	MaxSteps     int
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
	Stop    string          // why the model stopped: StopEnd, StopToolUse, …; "" = decide by ToolCalls
	Raw     json.RawMessage // the provider's original reply, sent back as is (keeps thinking blocks)
	RawFrom string          // the provider that produced Raw, e.g. "anthropic"
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

// Result is the outcome of a finished task.
type Result struct {
	Text  string // the model's final answer
	Steps int    // model calls made
}

// StepKind says what a saved step records.
type StepKind string

const (
	ModelReply StepKind = "model_reply" // the model's reply
	Intent     StepKind = "intent"      // about to run a tool call
	ToolResult StepKind = "tool_result" // the tool call's result
)

// Step is one entry in a task's append-only log.
type Step struct {
	Kind   StepKind
	CallID string  // tool call ID; empty for model replies
	Msg    Message // the reply or tool result; empty for intents
}

// Store persists a task's steps so Run can resume after a crash.
type Store interface {
	Append(ctx context.Context, taskID string, s Step) error // durable when it returns
	Load(ctx context.Context, taskID string) ([]Step, error) // in append order
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
func (r *Runner) Run(ctx context.Context, task Task) (Result, error) {
	t, err := r.start(ctx, task)
	if err != nil {
		return Result{}, fmt.Errorf("task %s: %w", task.ID, err)
	}
	text, err := t.run(ctx)
	if err != nil {
		return Result{Steps: t.steps}, fmt.Errorf("task %s: %w", task.ID, err)
	}
	return Result{Text: text, Steps: t.steps}, nil
}

// taskRun is the state of one Run.
type taskRun struct {
	r     *Runner
	task  Task
	tools []ToolDef       // the task's allowed tools
	msgs  []Message       // the conversation so far
	done  map[string]bool // IDs of tool calls with a saved result
	steps int             // model replies so far
	last  *Message        // the last saved reply when Run started; nil for a new task
}

// start checks the runner and the task, and rebuilds the task's state from the store.
func (r *Runner) start(ctx context.Context, task Task) (*taskRun, error) {
	if r.Model == nil || r.Tools == nil || r.Store == nil {
		return nil, errors.New("runner needs Model, Tools and Store")
	}
	if task.MaxSteps <= 0 {
		return nil, errors.New("max steps must be > 0")
	}
	if task.Model == "" {
		return nil, errors.New("model must be specified")
	}

	all, err := r.Tools.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	tools := filterTools(all, task.AllowedTools)
	if len(tools) == 0 {
		return nil, errors.New("no allowed tools available")
	}

	history, err := r.Store.Load(ctx, task.ID)
	if err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}
	t := &taskRun{r: r, task: task, tools: tools}
	t.replay(history)
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
	reply, err := t.r.Model.Ask(ctx, t.task.Model, t.msgs, t.tools)
	if err != nil {
		return Message{}, fmt.Errorf("step %d: ask model: %w", t.steps, err)
	}
	if err := t.r.Store.Append(ctx, t.task.ID, Step{Kind: ModelReply, Msg: reply}); err != nil {
		return Message{}, fmt.Errorf("save reply: %w", err)
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
			continue // ran before the restart
		}
		out, failed, err := t.callTool(ctx, call)
		if err != nil {
			return err
		}
		res := Message{Role: "tool", Text: out, ToolCallID: call.ID, IsError: failed}
		// The call has run, so save its result even if ctx was cancelled meanwhile;
		// otherwise resume would run it again.
		if err := t.r.Store.Append(context.WithoutCancel(ctx), t.task.ID, Step{Kind: ToolResult, CallID: call.ID, Msg: res}); err != nil {
			return fmt.Errorf("save result: %w", err)
		}
		t.msgs = append(t.msgs, res)
		t.done[call.ID] = true
	}
	return nil
}

// callTool runs call and returns the text for its tool result. A call to a tool
// the task doesn't allow is not run, and a failed call becomes an error text,
// both with failed set; the model sees them and can adjust. It returns an
// error only when the intent can't be saved or ctx is done. Then no result is
// saved, and resume handles the call again.
func (t *taskRun) callTool(ctx context.Context, call ToolCall) (out string, failed bool, err error) {
	if !slices.ContainsFunc(t.tools, func(d ToolDef) bool { return d.Name == call.Name }) {
		return fmt.Sprintf("tool %s is not available", call.Name), true, nil
	}
	// Intent before the side effect. On resume, an intent without a
	// result is run again; safe only for idempotent tools (approval later).
	if err := t.r.Store.Append(ctx, t.task.ID, Step{Kind: Intent, CallID: call.ID}); err != nil {
		return "", false, fmt.Errorf("save intent: %w", err)
	}
	out, err = t.r.Tools.Call(ctx, call)
	if err != nil {
		if ctx.Err() != nil {
			return "", false, fmt.Errorf("call %s interrupted: %w", call.Name, ctx.Err())
		}
		return fmt.Sprintf("calling tool %s: %v", call.Name, err), true, nil
	}
	return out, false, nil
}

// replay rebuilds the conversation from the steps saved by earlier runs.
func (t *taskRun) replay(history []Step) {
	t.msgs = []Message{{Role: "user", Text: t.task.Goal}}
	t.done = map[string]bool{}
	for _, s := range history {
		switch s.Kind {
		case ModelReply:
			t.msgs = append(t.msgs, s.Msg)
			t.steps++
			m := s.Msg
			t.last = &m
		case ToolResult:
			t.msgs = append(t.msgs, s.Msg)
			t.done[s.CallID] = true
		}
	}
}

// filterTools returns the tools from all whose names are in allowed.
func filterTools(all []ToolDef, allowed []string) []ToolDef {
	var out []ToolDef
	for _, t := range all {
		if slices.Contains(allowed, t.Name) {
			out = append(out, t)
		}
	}
	return out
}
