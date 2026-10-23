package model

import (
	"context"
	"slices"
	"testing"

	"github.com/jrniemiec/llm"

	"github.com/jrniemiec/keel/runner"
)

// fakeProvider returns the stop reasons in stops, one per call, and records
// the output limit of each call.
type fakeProvider struct {
	limit int
	stops *[]string
	calls *[]int
}

func (p fakeProvider) Name() string { return "fake" }
func (p fakeProvider) Chat(context.Context, string, []llm.Message) (string, llm.Usage, error) {
	return "", llm.Usage{}, nil
}
func (p fakeProvider) ChatStream(context.Context, string, []llm.Message, func(string) error) (string, llm.Usage, error) {
	return "", llm.Usage{}, nil
}
func (p fakeProvider) ChatTools(context.Context, string, []llm.Turn, []llm.ToolDef) (llm.ToolReply, error) {
	*p.calls = append(*p.calls, p.limit)
	stop := (*p.stops)[0]
	*p.stops = (*p.stops)[1:]
	return llm.ToolReply{Turn: llm.Turn{Role: llm.RoleAssistant, Text: "x"}, Stop: stop}, nil
}

// newFakeLLM returns an LLM whose providers reply with stops in order, and the
// output limits they were called with.
func newFakeLLM(configs map[string]llm.ProviderConfig, stops ...string) (*LLM, *[]int) {
	var calls []int
	m := NewLLM(configs, "")
	m.newProvider = func(cfg llm.ProviderConfig) (llm.Provider, error) {
		return fakeProvider{limit: cfg.MaxOutputTokens, stops: &stops, calls: &calls}, nil
	}
	return m, &calls
}

func ask(t *testing.T, m *LLM, model string) runner.Message {
	t.Helper()
	reply, err := m.Ask(context.Background(), model, []runner.Message{{Role: "user", Text: "q"}}, nil)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	return reply
}

func TestAskRetriesCutOffWithLargerLimit(t *testing.T) {
	m, calls := newFakeLLM(map[string]llm.ProviderConfig{"anthropic": {MaxOutputTokens: 16000}},
		llm.StopMaxTokens, llm.StopEnd)
	reply := ask(t, m, "anthropic:claude-test")

	if reply.Stop != runner.StopEnd {
		t.Errorf("Stop = %q, want the retry's %q", reply.Stop, runner.StopEnd)
	}
	if want := []int{16000, 32000}; !slices.Equal(*calls, want) {
		t.Errorf("limits = %v, want %v", *calls, want)
	}
}

// A second cut-off is returned; the runner then fails the task.
func TestAskRetriesOnlyOnce(t *testing.T) {
	m, calls := newFakeLLM(map[string]llm.ProviderConfig{"anthropic": {MaxOutputTokens: 1000}},
		llm.StopMaxTokens, llm.StopMaxTokens)
	reply := ask(t, m, "anthropic:claude-test")

	if reply.Stop != runner.StopMaxTokens {
		t.Errorf("Stop = %q, want %q", reply.Stop, runner.StopMaxTokens)
	}
	if want := []int{1000, 2000}; !slices.Equal(*calls, want) {
		t.Errorf("limits = %v, want %v", *calls, want)
	}
}

// llm sends no output limit to other providers, so a retry wouldn't help.
func TestAskNoRetryForOtherProviders(t *testing.T) {
	m, calls := newFakeLLM(nil, llm.StopMaxTokens)
	reply := ask(t, m, "openai:gpt-test")

	if reply.Stop != runner.StopMaxTokens || len(*calls) != 1 {
		t.Errorf("Stop = %q after %d calls, want %q after 1", reply.Stop, len(*calls), runner.StopMaxTokens)
	}
	if (*calls)[0] != 0 {
		t.Errorf("limit = %d, want 0 (not set for openai)", (*calls)[0])
	}
}

// Consecutive tool results become one user turn.
func TestToTurnsGroupsResults(t *testing.T) {
	msgs := []runner.Message{
		{Role: "user", Text: "q"},
		{Role: "assistant", ToolCalls: []runner.ToolCall{{ID: "a", Name: "t"}, {ID: "b", Name: "t"}}},
		{Role: "tool", ToolCallID: "a", Text: "1"},
		{Role: "tool", ToolCallID: "b", Text: "boom", IsError: true},
		{Role: "assistant", ToolCalls: []runner.ToolCall{{ID: "c", Name: "t"}}},
		{Role: "tool", ToolCallID: "c", Text: "3"},
	}
	turns, err := toTurns(msgs)
	if err != nil {
		t.Fatalf("toTurns: %v", err)
	}
	if len(turns) != 5 {
		t.Fatalf("got %d turns, want 5: %+v", len(turns), turns)
	}
	if r := turns[2].Results; len(r) != 2 || r[0].CallID != "a" || !r[1].IsError {
		t.Errorf("turn 2 results = %+v, want a and b (error)", r)
	}
	if r := turns[4].Results; len(r) != 1 || r[0].CallID != "c" {
		t.Errorf("turn 4 results = %+v, want c only", r)
	}
}
