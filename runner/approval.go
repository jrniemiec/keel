package runner

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jrniemiec/keel/internal/klog"
)

// Tool classes, from a tool's effective Hints (see ToolClass).
const (
	ClassRead        = "read"        // read-only
	ClassWrite       = "write"       // changes something, not destructive
	ClassDestructive = "destructive" // may delete or overwrite
)

// Approval values.
const (
	ApprovalAuto = "auto" // the call runs
	ApprovalAsk  = "ask"  // a human decides first
)

// ReasonOutcomeUnknown is the reason for asking about a call that may have
// run before a restart: it has an intent and no result, and its tool is
// neither read-only nor idempotent. It is asked whatever the Approval says.
const ReasonOutcomeUnknown = "outcome unknown"

// Why a call may run, in its intent's Msg.Text and the tool call log entry.
// A rerun of an intent from before a restart that is safe to repeat saves no
// new intent; it is logged with AllowedRerunSafe.
const (
	AllowedAuto          = "auto: "          // + the tool's class: the Approval says "auto"
	AllowedApproved      = "approved"        // after an approved step (asked, or approved for this run)
	AllowedRerunApproved = "rerun: approved" // outcome unknown, and the user approved repeating it
	AllowedRerunSafe     = "rerun: safe"     // read-only or idempotent
)

// Approval says, per tool class, whether a call runs ("auto") or asks first
// ("ask").
type Approval struct {
	Read        string `json:"read"`
	Write       string `json:"write"`
	Destructive string `json:"destructive"`
}

// For returns the setting for class.
func (a Approval) For(class string) string {
	switch class {
	case ClassRead:
		return a.Read
	case ClassWrite:
		return a.Write
	default:
		return a.Destructive
	}
}

// Check reports a value other than "auto" or "ask".
func (a Approval) Check() error {
	var errs []error
	for _, class := range []string{ClassRead, ClassWrite, ClassDestructive} {
		if v := a.For(class); v != ApprovalAuto && v != ApprovalAsk {
			errs = append(errs, fmt.Errorf("approval %s %q: want auto or ask", class, v))
		}
	}
	return errors.Join(errs...)
}

// ToolClass returns the class of a tool with hints h. ReadOnly wins over
// Destructive: MCP defines destructiveHint only for tools that aren't
// read-only.
func ToolClass(h Hints) string {
	switch {
	case h.ReadOnly:
		return ClassRead
	case h.Destructive:
		return ClassDestructive
	default:
		return ClassWrite
	}
}

// Approver asks a human whether a tool call may run. Ask blocks until the
// answer or ctx is done. An error wrapping ErrApprovalNeeded means no answer
// can be had (e.g. stdin closed): the task waits.
type Approver interface {
	Ask(ctx context.Context, req ApprovalRequest) (Decision, error)
}

// ApprovalRequest is what the Approver shows.
type ApprovalRequest struct {
	Call   ToolCall
	Tool   ToolDef // the snapshot's definition, with the effective hints
	Reason string  // the tool's class, e.g. "destructive", or ReasonOutcomeUnknown
}

// Decision is a human's answer.
type Decision struct {
	Approved bool
	ForRun   bool   // approved for the rest of this run: later calls to the tool don't ask
	Message  string // optional, for the model when not approved
}

// ErrApprovalNeeded is returned by Run when a call needs approval and there is
// no Approver, or it can't get an answer. The task is waiting.
var ErrApprovalNeeded = errors.New("waiting for approval")

// forRunText is the text of an approved step that no one was asked for:
// the tool was approved for the rest of the run.
const forRunText = "approved for this run"

// approvalReason says why call needs a decision, or "" if it doesn't: a
// pending call (intent, no result) not safe to repeat is ReasonOutcomeUnknown;
// otherwise the Approval for the tool's class decides. A decided call is
// handled before.
func (t *taskRun) approvalReason(call ToolCall, tool ToolDef) string {
	if t.pending[call.ID] {
		if t.safeToRepeat(call.Name) {
			return ""
		}
		return ReasonOutcomeUnknown
	}
	class := ToolClass(tool.Hints)
	if t.task.Approval.For(class) == ApprovalAsk {
		return class
	}
	return ""
}

// decide gets a decision for call: for a tool approved for this run it saves
// an approved step and asks no one; otherwise it saves approval_asked
// (unless asked already), asks the Approver and saves approved or rejected.
// Without an Approver it returns ErrApprovalNeeded.
func (t *taskRun) decide(ctx context.Context, call ToolCall, tool ToolDef, reason string) (Decision, error) {
	if reason != ReasonOutcomeUnknown && t.approvedForRun[call.Name] {
		if err := t.saveDecision(ctx, call, Approved, forRunText); err != nil {
			return Decision{}, err
		}
		t.log.Info(klog.MsgToolApproved, "seq", t.seq, "tool", call.Name, "call_id", call.ID, "for_run", true)
		return Decision{Approved: true}, nil
	}
	if _, ok := t.asked[call.ID]; !ok {
		step := Step{Kind: ApprovalAsked, CallID: call.ID, Msg: Message{Text: reason, ToolCalls: []ToolCall{call}}}
		if err := t.save(ctx, step); err != nil {
			return Decision{}, fmt.Errorf("save approval asked: %w", err)
		}
		t.asked[call.ID] = reason
		t.log.Info(klog.MsgApprovalAsked, "seq", t.seq, "kind", ApprovalAsked, "tool", call.Name, "call_id", call.ID, "reason", reason)
	}
	if t.r.Approver == nil {
		return Decision{}, fmt.Errorf("%w: %s (call %s)", ErrApprovalNeeded, call.Name, call.ID)
	}
	began := time.Now()
	d, err := t.r.Approver.Ask(ctx, ApprovalRequest{Call: call, Tool: tool, Reason: reason})
	t.waited += time.Since(began)
	if err != nil {
		if ctx.Err() != nil {
			return Decision{}, fmt.Errorf("approval of %s interrupted: %w", call.Name, ctx.Err())
		}
		return Decision{}, fmt.Errorf("approval of %s (call %s): %w", call.Name, call.ID, err)
	}
	if reason == ReasonOutcomeUnknown {
		d.ForRun = false // about this one call only
	}
	kind, text := Rejected, d.Message
	if d.Approved {
		kind, text = Approved, ""
		if d.ForRun {
			text = forRunText
			t.approvedForRun[call.Name] = true
		}
	}
	if err := t.saveDecision(ctx, call, kind, text); err != nil {
		return Decision{}, err
	}
	if d.Approved {
		t.log.Info(klog.MsgToolApproved, "seq", t.seq, "tool", call.Name, "call_id", call.ID, "for_run", d.ForRun)
	} else {
		t.log.Info(klog.MsgToolRejected, "seq", t.seq, "tool", call.Name, "call_id", call.ID, "message", d.Message)
	}
	return d, nil
}

// saveDecision saves an approved or rejected step for call. The decision is
// saved before the intent: a resume finds it and doesn't ask again.
func (t *taskRun) saveDecision(ctx context.Context, call ToolCall, kind StepKind, text string) error {
	step := Step{Kind: kind, CallID: call.ID, Msg: Message{Text: text, ToolCalls: []ToolCall{call}}}
	if err := t.save(ctx, step); err != nil {
		return fmt.Errorf("save %s: %w", kind, err)
	}
	t.decided[call.ID] = step
	return nil
}

// rejectedText is the tool result the model gets for a rejected call.
func rejectedText(reason, message string) string {
	text := "not run: rejected by the user"
	if reason == ReasonOutcomeUnknown {
		text = "not run again: it may have run before a restart; the user rejected repeating it"
	}
	if message != "" {
		text += ": " + message
	}
	return text
}

// tool returns the task's tool name.
func (t *taskRun) tool(name string) (ToolDef, bool) {
	i := slices.IndexFunc(t.tools, func(d ToolDef) bool { return d.Name == name })
	if i < 0 {
		return ToolDef{}, false
	}
	return t.tools[i], true
}
