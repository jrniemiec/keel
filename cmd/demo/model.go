package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jrniemiec/keel/runner"
)

// scriptModel plays the model with a fixed script. It picks its reply by how
// many replies are already in the conversation, so it continues correctly
// after a resume.
type scriptModel struct {
	report string
}

func (m *scriptModel) Ask(ctx context.Context, model string, msgs []runner.Message, tools []runner.ToolDef) (runner.Message, error) {
	switch countReplies(msgs) {
	case 0:
		return toolCalls(call("c1", "read_file", map[string]string{"path": "notes.txt"})), nil
	case 1:
		text := resultOf(msgs, "c1")
		return toolCalls(
			call("c2", "count_words", map[string]string{"text": text}),
			call("c3", "count_lines", map[string]string{"text": text}),
		), nil
	case 2:
		line := fmt.Sprintf("notes.txt: %s words, %s lines\n", resultOf(msgs, "c2"), resultOf(msgs, "c3"))
		return toolCalls(call("c4", "append_file", map[string]string{"path": m.report, "text": line})), nil
	default:
		answer := fmt.Sprintf("Wrote %s words and %s lines to %s.", resultOf(msgs, "c2"), resultOf(msgs, "c3"), m.report)
		return runner.Message{Role: "assistant", Text: answer}, nil
	}
}

func countReplies(msgs []runner.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == "assistant" {
			n++
		}
	}
	return n
}

// resultOf returns the result text of the tool call with the given ID.
func resultOf(msgs []runner.Message, callID string) string {
	for _, m := range msgs {
		if m.ToolCallID == callID {
			return m.Text
		}
	}
	return ""
}

func call(id, name string, args map[string]string) runner.ToolCall {
	b, _ := json.Marshal(args)
	return runner.ToolCall{ID: id, Name: name, Args: b}
}

func toolCalls(calls ...runner.ToolCall) runner.Message {
	return runner.Message{Role: "assistant", ToolCalls: calls}
}
