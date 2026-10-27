package runner

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jrniemiec/keel/internal/klog"
)

// The context window: what's sent to the model is the conversation with two
// changes, while the Store keeps it whole (local/design-notes.md, "Context
// window: tool result cap and clearing"):
//   - a tool result longer than Task.MaxToolResultTokens is cut;
//   - old tool results are cleared once the history passes
//     Task.ClearToolResultsAtPercent of the model's window (clearOld).

// ErrContextFull is returned by Run, and by a Model, when the conversation
// doesn't fit the model's context window. The task waits, like for any
// limit: clear results earlier, cap them, or use a larger window, then run
// it again.
var ErrContextFull = fmt.Errorf("%w: context window full", ErrLimitReached)

const (
	bytesPerToken = 4  // for estimates, as llm.ApproxTokens
	keepResults   = 5  // the newest tool results are never cleared
	clearPoints   = 20 // clearing goes this many percent points below the threshold
	clearedText   = "[cleared; call the tool again if needed]"
)

// approxTokens estimates the tokens in s.
func approxTokens(s string) int { return (len(s) + bytesPerToken - 1) / bytesPerToken }

// approxMsgTokens estimates the tokens m adds to a request. An assistant
// message is sent as its Raw when it has one (thinking included).
func approxMsgTokens(m Message) int {
	if len(m.Raw) > 0 {
		return approxTokens(string(m.Raw))
	}
	n := approxTokens(m.Text)
	for _, c := range m.ToolCalls {
		n += approxTokens(c.Name) + approxTokens(string(c.Args))
	}
	return n
}

// addReply adds a model reply to the conversation and remembers its
// PromptTokens, the base of the next size estimate.
func (t *taskRun) addReply(reply Message) {
	t.promptTokens = reply.PromptTokens
	t.promptMsgs = len(t.msgs)
	t.msgs = append(t.msgs, reply)
}

// sent returns the conversation as it's sent to the model: cleared results
// replaced with clearedText, the rest cut to Task.MaxToolResultTokens.
func (t *taskRun) sent() []Message {
	out := slices.Clone(t.msgs)
	names := callNames(out)
	for i := range out {
		m := &out[i]
		if m.Role != "tool" {
			continue
		}
		if i <= t.clearedThrough && t.clearable(names[m.ToolCallID]) {
			m.Text = clearedText
			continue
		}
		m.Text = capText(m.Text, t.task.MaxToolResultTokens)
	}
	return out
}

// clearOld clears old tool results from what's sent when the history passes
// Task.ClearToolResultsAtPercent of the window: the oldest clearable ones,
// until it is clearPoints below that, keeping the newest keepResults. The
// point reached is saved (ResultsCleared), so a resume sends the same
// history; it only moves forward, so the start of the history stays the same
// between clears (prompt cache). Nothing happens for an unknown window.
func (t *taskRun) clearOld(ctx context.Context) error {
	pct := t.task.ClearToolResultsAtPercent
	if pct == 0 || t.window == 0 {
		return nil
	}
	sent := t.sent()
	size := t.historyTokens(sent)
	threshold := t.window * pct / 100
	if size <= threshold {
		return nil
	}
	target := t.window * max(pct-clearPoints, 0) / 100

	var results []int // indexes of the tool results in msgs
	for i, m := range t.msgs {
		if m.Role == "tool" {
			results = append(results, i)
		}
	}
	names := callNames(t.msgs)
	before := size
	cleared, freed, last := 0, 0, 0
	for _, i := range results[:max(len(results)-keepResults, 0)] {
		if size <= target {
			break
		}
		if i <= t.clearedThrough || !t.clearable(names[t.msgs[i].ToolCallID]) {
			continue
		}
		d := approxTokens(sent[i].Text) - approxTokens(clearedText)
		size -= d
		freed += d
		cleared++
		last = i
	}
	if cleared == 0 {
		if !t.warnedNoneClear {
			t.log.Warn(klog.MsgNothingToClear, "size", size, "threshold", threshold)
			t.warnedNoneClear = true
		}
		return nil
	}

	text := fmt.Sprintf("%d tool results, ~%s tokens", cleared, formatTokens(freed))
	if err := t.save(ctx, Step{Kind: ResultsCleared, CallID: t.msgs[last].ToolCallID, Msg: Message{Text: text}}); err != nil {
		return fmt.Errorf("save cleared results: %w", err)
	}
	t.clearedThrough = last
	t.log.Info(klog.MsgResultsCleared, "seq", t.seq, "results", cleared, "tokens", freed,
		"size", before, "size_after", size, "threshold", threshold)
	t.progress("clear  %s: history ~%s → ~%s of %s", text, formatTokens(before), formatTokens(size), formatTokens(t.window))
	return nil
}

// historyTokens estimates the tokens of sent: the last reply's PromptTokens
// for the messages it was asked with, plus an estimate for the newer ones.
// Without PromptTokens (a task saved before it), all is estimated, tool
// definitions included.
func (t *taskRun) historyTokens(sent []Message) int {
	n, from := 0, 0
	if t.promptTokens > 0 && t.promptMsgs <= len(sent) {
		n, from = t.promptTokens, t.promptMsgs
	} else {
		for _, d := range t.tools {
			n += approxTokens(d.Name) + approxTokens(d.Description) + approxTokens(string(d.InputSchema))
		}
	}
	for _, m := range sent[from:] {
		n += approxMsgTokens(m)
	}
	return n
}

// clearable reports whether results of the tool name may be cleared: only a
// read tool's. A write or destructive tool's result is short, and with its
// call it records what was done.
func (t *taskRun) clearable(name string) bool {
	i := slices.IndexFunc(t.tools, func(d ToolDef) bool { return d.Name == name })
	return i >= 0 && ToolClass(t.tools[i].Hints) == ClassRead
}

// resultIndex returns the index in msgs of the tool result for callID; 0 if
// there is none.
func (t *taskRun) resultIndex(callID string) int {
	for i := len(t.msgs) - 1; i > 0; i-- {
		if t.msgs[i].Role == "tool" && t.msgs[i].ToolCallID == callID {
			return i
		}
	}
	return 0
}

// callNames returns the tool name of each call in msgs, by call ID.
func callNames(msgs []Message) map[string]string {
	names := map[string]string{}
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			names[c.ID] = c.Name
		}
	}
	return names
}

// capText returns text cut to about maxTokens tokens, with a note saying so;
// at a line end when there is one in the second half. maxTokens 0: no cap.
func capText(text string, maxTokens int) string {
	maxBytes := maxTokens * bytesPerToken
	if maxTokens == 0 || len(text) <= maxBytes {
		return text
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	if nl := strings.LastIndexByte(text[:cut], '\n'); nl >= cut/2 {
		cut = nl
	}
	return text[:cut] + fmt.Sprintf("\n[cut: first ~%s of ~%s tokens; ask for a smaller part]",
		formatTokens(approxTokens(text[:cut])), formatTokens(approxTokens(text)))
}

// historyText returns the history-size part of a model's progress line: what
// was sent, and the model's context window, e.g. "  history 84K of 200K
// (42%)"; without a known window "  history 84K"; "" without promptTokens.
func (t *taskRun) historyText(promptTokens int) string {
	switch {
	case promptTokens == 0:
		return ""
	case t.window > 0:
		return fmt.Sprintf("  history %s of %s (%s)", formatTokens(promptTokens), formatTokens(t.window),
			formatPercent(float64(promptTokens)/float64(t.window)))
	default:
		return "  history " + formatTokens(promptTokens)
	}
}

// formatTokens returns n as "840", "1.2K", "84K" or "1.2M".
func formatTokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 10_000:
		return oneDecimal(float64(n)/1e3) + "K"
	case n < 1_000_000:
		return fmt.Sprintf("%dK", (n+500)/1000)
	default:
		return oneDecimal(float64(n)/1e6) + "M"
	}
}

// formatPercent returns the fraction f as "0.4%" below 10%, else "42%".
func formatPercent(f float64) string {
	if p := 100 * f; p < 10 {
		return oneDecimal(p) + "%"
	}
	return fmt.Sprintf("%.0f%%", 100*f)
}

// oneDecimal returns v with one decimal, without a trailing ".0".
func oneDecimal(v float64) string {
	return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0")
}
