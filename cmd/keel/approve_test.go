package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jrniemiec/keel/runner"
)

func request(reason string) runner.ApprovalRequest {
	return runner.ApprovalRequest{
		Call:   runner.ToolCall{ID: "c1", Name: "fs__write_file", Args: json.RawMessage(`{"path":"r.txt","content":"a\nb\nc\nd\ne"}`)},
		Reason: reason,
	}
}

func TestTerminalApprover(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		input  string
		want   runner.Decision
		output string // in what was written
	}{
		{"yes", "destructive", "y\n", runner.Decision{Approved: true}, "Allow? [y]es / [a]lways this run"},
		{"always", "destructive", "A\n", runner.Decision{Approved: true, ForRun: true}, ""},
		{"no", "destructive", "n\n", runner.Decision{}, ""},
		{"empty is no", "destructive", "\n", runner.Decision{}, ""},
		{"message", "destructive", "m\n write to b \n", runner.Decision{Message: "write to b"}, "Message for the model: "},
		{"unknown answer, asked again", "destructive", "x\ny\n", runner.Decision{Approved: true}, "Answer y, a, n, m or v."},
		{"view shows all", "destructive", "v\ny\n", runner.Decision{Approved: true}, "           d\n           e\n"},
		{"outcome unknown has no always", runner.ReasonOutcomeUnknown, "a\ny\n", runner.Decision{Approved: true}, "Answer y, n, m or v."},
		{"outcome unknown warns", runner.ReasonOutcomeUnknown, "y\n", runner.Decision{Approved: true}, "may repeat its effect"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			a := newTerminalApprover(strings.NewReader(tt.input), &out)
			got, err := a.Ask(context.Background(), request(tt.reason))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("decision = %+v, want %+v", got, tt.want)
			}
			if !strings.Contains(out.String(), tt.output) {
				t.Errorf("output lacks %q:\n%s", tt.output, out.String())
			}
		})
	}
}

func TestTerminalApproverShowsCall(t *testing.T) {
	var out bytes.Buffer
	a := newTerminalApprover(strings.NewReader("y\n"), &out)
	if _, err := a.Ask(context.Background(), request("destructive")); err != nil {
		t.Fatal(err)
	}
	want := "\nfs__write_file  destructive  (call c1)\n" +
		"  path:    r.txt\n" +
		"  content: a\n" +
		"           b\n" +
		"           c\n" +
		"           … 2 more lines (v shows all)\n"
	if !strings.HasPrefix(out.String(), want) {
		t.Errorf("output:\n%s\nwant it to start with:\n%s", out.String(), want)
	}
}

func TestTerminalApproverNoAnswer(t *testing.T) {
	a := newTerminalApprover(strings.NewReader(""), io.Discard)
	if _, err := a.Ask(context.Background(), request("destructive")); !errors.Is(err, runner.ErrApprovalNeeded) {
		t.Errorf("stdin closed: err = %v, want ErrApprovalNeeded", err)
	}

	r, w := io.Pipe() // no answer comes
	defer w.Close()
	a = newTerminalApprover(r, io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Ask(ctx, request("destructive")); !errors.Is(err, context.Canceled) {
		t.Errorf("ctx done: err = %v, want canceled", err)
	}
}

func TestFormatArgs(t *testing.T) {
	long := strings.Repeat("x", lineChars+5)
	tests := []struct {
		name, args string
		maxLines   int
		want       string
	}{
		{"order kept, non-strings as JSON", `{"b":1,"a":[1,2]}`, argLines, "  b: 1\n  a: [1,2]\n"},
		{"long line cut", `{"s":"` + long + `"}`, argLines, "  s: " + long[:lineChars] + " … 5 more characters (v shows all)\n"},
		{"one more line", `{"s":"a\nb\nc\nd"}`, argLines, "  s: a\n     b\n     c\n     … 1 more line (v shows all)\n"},
		{"all", `{"s":"` + long + `"}`, 0, "  s: " + long + "\n"},
		{"not an object", `[1]`, argLines, "  [1]\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatArgs(json.RawMessage(tt.args), tt.maxLines); got != tt.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}

func TestStepsShowsApprovals(t *testing.T) {
	c1 := runner.ToolCall{ID: "c1", Name: "fs__write_file", Args: json.RawMessage(`{"path":"r.txt"}`)}
	c2 := runner.ToolCall{ID: "c2", Name: "fs__delete_file", Args: json.RawMessage(`{"path":"r.txt"}`)}
	step := func(kind runner.StepKind, c runner.ToolCall, text string) runner.Step {
		return runner.Step{Kind: kind, CallID: c.ID, Msg: runner.Message{Text: text, ToolCalls: []runner.ToolCall{c}}}
	}
	history := []runner.Step{
		step(runner.ApprovalAsked, c1, "destructive"),
		step(runner.Approved, c1, "approved for this run"),
		step(runner.Intent, c1, "approved"),
		step(runner.ApprovalAsked, c2, "destructive"),
		step(runner.Rejected, c2, "keep it"),
		{Kind: runner.ToolResult, CallID: "c2", Msg: runner.Message{Role: "tool", Text: "not run: rejected by the user: keep it", IsError: true}},
	}
	var out bytes.Buffer
	if err := printSteps(&out, history, false); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"1    approval_asked  fs__write_file destructive",
		`2    approved        fs__write_file "approved for this run"`,
		"3    intent          fs__write_file (approved)",
		`5    rejected        fs__delete_file "keep it"`,
		`6    tool_result     fs__delete_file error "not run: rejected by the user: keep it"`,
	} {
		if !strings.Contains(out.String(), w+"\n") {
			t.Errorf("output lacks %q:\n%s", w, out.String())
		}
	}
}
