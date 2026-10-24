package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/jrniemiec/keel/runner"
)

// terminalApprover asks at the terminal whether a tool call may run. It
// implements runner.Approver.
type terminalApprover struct {
	out   io.Writer
	lines <-chan string // lines read from the terminal; closed at EOF
}

// newTerminalApprover reads answers from in and writes to out. One goroutine
// reads in for the whole process, so an Ask that ends with ctx (Ctrl-C)
// leaves no read behind that would take the next answer.
func newTerminalApprover(in io.Reader, out io.Writer) *terminalApprover {
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(in)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	return &terminalApprover{out: out, lines: lines}
}

// argLines and lineChars are how much of one argument the prompt shows; v
// shows all.
const (
	argLines  = 3
	lineChars = 200
)

// Ask shows the call and reads the answer: y runs it, a runs it and later
// calls to the tool in this run, n (or empty) doesn't, m doesn't and asks for
// a message for the model, v shows the args in full and asks again.
func (a *terminalApprover) Ask(ctx context.Context, req runner.ApprovalRequest) (runner.Decision, error) {
	unknown := req.Reason == runner.ReasonOutcomeUnknown
	fmt.Fprintf(a.out, "\n%s  %s  (call %s)\n", req.Call.Name, req.Reason, req.Call.ID)
	if unknown {
		fmt.Fprintln(a.out, "  It may have run before keel stopped; running it again may repeat its effect.")
	}
	fmt.Fprint(a.out, formatArgs(req.Call.Args, argLines))
	question := "Allow? [y]es / [a]lways this run / [n]o / [m]essage, then no / [v]iew all: "
	if unknown {
		question = "Run again? [y]es / [n]o / [m]essage, then no / [v]iew all: "
	}
	for {
		fmt.Fprint(a.out, question)
		answer, err := a.readLine(ctx)
		if err != nil {
			return runner.Decision{}, err
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "y", "yes":
			return runner.Decision{Approved: true}, nil
		case "a", "always":
			if !unknown {
				return runner.Decision{Approved: true, ForRun: true}, nil
			}
		case "", "n", "no":
			return runner.Decision{}, nil
		case "m", "message":
			fmt.Fprint(a.out, "Message for the model: ")
			msg, err := a.readLine(ctx)
			if err != nil {
				return runner.Decision{}, err
			}
			return runner.Decision{Message: strings.TrimSpace(msg)}, nil
		case "v", "view":
			fmt.Fprint(a.out, formatArgs(req.Call.Args, 0))
			continue
		}
		if unknown {
			fmt.Fprintln(a.out, "Answer y, n, m or v.")
		} else {
			fmt.Fprintln(a.out, "Answer y, a, n, m or v.")
		}
	}
}

// readLine returns the next line from the terminal. At EOF no answer can be
// had: the task waits (runner.ErrApprovalNeeded).
func (a *terminalApprover) readLine(ctx context.Context) (string, error) {
	select {
	case line, ok := <-a.lines:
		if !ok {
			fmt.Fprintln(a.out)
			return "", fmt.Errorf("%w: no answer, stdin closed", runner.ErrApprovalNeeded)
		}
		return line, nil
	case <-ctx.Done():
		fmt.Fprintln(a.out)
		return "", ctx.Err()
	}
}

// formatArgs returns args one top-level key per line, in the model's order,
// values aligned; a string is shown as text, other values as JSON. Each value
// is cut to maxLines lines (0 = all). Args that aren't a JSON object are
// shown as they are.
func formatArgs(args json.RawMessage, maxLines int) string {
	keys, values, ok := objectFields(args)
	if !ok {
		return "  " + cutLines(string(args), maxLines, "  ") + "\n"
	}
	width := 0
	for _, k := range keys {
		width = max(width, len(k)+1)
	}
	var b strings.Builder
	for i, k := range keys {
		text := string(values[i])
		var s string
		if json.Unmarshal(values[i], &s) == nil {
			text = s
		}
		indent := strings.Repeat(" ", 2+width+1)
		fmt.Fprintf(&b, "  %-*s %s\n", width, k+":", cutLines(text, maxLines, indent))
	}
	return b.String()
}

// objectFields returns the keys and values of a JSON object, in order.
func objectFields(data json.RawMessage) (keys []string, values []json.RawMessage, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, nil, false
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil, false
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil, false
		}
		keys, values = append(keys, tok.(string)), append(values, v)
	}
	return keys, values, true
}

// cutLines returns text with lines after the first indented. With maxLines
// > 0 it keeps that many lines, each cut to lineChars characters, with a
// note of what was left out.
func cutLines(text string, maxLines int, indent string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if maxLines == 0 {
		return strings.Join(lines, "\n"+indent)
	}
	note := ""
	if len(lines) > maxLines {
		n := len(lines) - maxLines
		note = fmt.Sprintf("\n%s… %d more %s (v shows all)", indent, n, plural(n, "line"))
		lines = lines[:maxLines]
	}
	for i, line := range lines {
		if r := []rune(line); len(r) > lineChars {
			n := len(r) - lineChars
			lines[i] = string(r[:lineChars]) + fmt.Sprintf(" … %d more %s (v shows all)", n, plural(n, "character"))
		}
	}
	return strings.Join(lines, "\n"+indent) + note
}

// plural returns word, with "s" unless n is 1.
func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
