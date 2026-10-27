// Package model connects the runner to real LLMs through github.com/jrniemiec/llm.
package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jrniemiec/llm"

	"github.com/jrniemiec/keel/internal/klog"
	"github.com/jrniemiec/keel/models"
	"github.com/jrniemiec/keel/runner"
)

// LLM implements runner.Model. Task.Model names the provider and the model as
// "provider:model", e.g. "anthropic:claude-sonnet-5", "openai:gpt-5" or
// "ollama:qwen3.5:9b". A provider is created on first use and reused.
//
// Each reply's USD comes from its tokens and the model's prices in the
// models file; a model not in it costs 0 (keel run checks it is there).
//
// A reply cut off at the output limit is asked again once with double the
// limit (Anthropic only), so the runner never sees the cut-off one. If that is
// cut off too, it's returned and the runner fails the task.
type LLM struct {
	configs     map[string]llm.ProviderConfig // per provider: API key, thinking, …; Model is set per task
	system      string
	models      models.Models                                  // prices, by provider:model
	newProvider func(llm.ProviderConfig) (llm.Provider, error) // llm.New; replaced in tests

	mu        sync.Mutex
	providers map[providerKey]llm.ToolProvider
}

type providerKey struct {
	name   string // "provider:model"
	larger bool   // double output limit, for a retry
}

// NewLLM returns an LLM. configs holds the settings for each provider name
// ("anthropic", "openai", "ollama"), from the keel config; a provider without
// an entry gets llm's defaults. system is the system prompt for every
// request; it may be empty. ms gives the prices.
func NewLLM(configs map[string]llm.ProviderConfig, system string, ms models.Models) *LLM {
	return &LLM{
		configs:     configs,
		system:      system,
		models:      ms,
		newProvider: llm.New,
		providers:   map[providerKey]llm.ToolProvider{},
	}
}

// Ask implements runner.Model.
func (m *LLM) Ask(ctx context.Context, model string, msgs []runner.Message, tools []runner.ToolDef) (runner.Message, error) {
	turns, err := toTurns(msgs)
	if err != nil {
		return runner.Message{}, err
	}
	defs := toToolDefs(tools)

	reply, err := m.chat(ctx, providerKey{name: model}, turns, defs)
	if errors.Is(err, llm.ErrContextFull) {
		// Asked again with a larger window, the same history fits.
		return runner.Message{}, fmt.Errorf("%w: context of %s full; raise providers.ollama.max_context_tokens in the keel config: %w",
			runner.ErrLimitReached, model, err)
	}
	if err != nil {
		return runner.Message{}, err
	}
	if reply.Stop == llm.StopMaxTokens && strings.HasPrefix(model, "anthropic:") {
		klog.Warn(klog.MsgMaxTokensRetry, "model", model, "out", reply.Usage.OutputTokens)
		first := reply.Usage
		reply, err = m.chat(ctx, providerKey{name: model, larger: true}, turns, defs)
		if err != nil {
			return runner.Message{}, fmt.Errorf("retry with a larger output limit: %w", err)
		}
		reply.Usage.InputTokens += first.InputTokens
		reply.Usage.OutputTokens += first.OutputTokens
	}
	msg := fromReply(reply)
	msg.USD = m.models[model].Cost(msg.TokensIn, msg.TokensOut)
	return msg, nil
}

func (m *LLM) chat(ctx context.Context, key providerKey, turns []llm.Turn, tools []llm.ToolDef) (llm.ToolReply, error) {
	p, err := m.provider(key)
	if err != nil {
		return llm.ToolReply{}, err
	}
	return p.ChatTools(ctx, m.system, turns, tools)
}

// provider returns the provider for key, creating it on first use.
func (m *LLM) provider(key providerKey) (llm.ToolProvider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.providers[key]; ok {
		return p, nil
	}

	kind, modelName, ok := strings.Cut(key.name, ":")
	if !ok || kind == "" || modelName == "" {
		return nil, fmt.Errorf("model %q: want provider:model, e.g. anthropic:claude-sonnet-5", key.name)
	}
	cfg := m.configs[kind]
	cfg.Provider = kind
	cfg.Model = modelName
	if key.larger {
		cfg.MaxOutputTokens *= 2
	}
	p, err := m.newProvider(cfg)
	if err != nil {
		return nil, fmt.Errorf("model %q: %w", key.name, err)
	}
	tp, ok := p.(llm.ToolProvider)
	if !ok {
		return nil, fmt.Errorf("model %q: provider %s has no tool calling", key.name, kind)
	}
	m.providers[key] = tp
	return tp, nil
}

// toTurns converts the conversation. Consecutive "tool" messages become one
// user turn with all their results, which is what the providers expect.
func toTurns(msgs []runner.Message) ([]llm.Turn, error) {
	turns := make([]llm.Turn, 0, len(msgs))
	for i, msg := range msgs {
		switch msg.Role {
		case "user":
			turns = append(turns, llm.Turn{Role: llm.RoleUser, Text: msg.Text})
		case "assistant":
			t := llm.Turn{Role: llm.RoleAssistant, Text: msg.Text, Raw: msg.Raw, RawFrom: msg.RawFrom}
			for _, c := range msg.ToolCalls {
				t.Calls = append(t.Calls, llm.ToolCall{ID: c.ID, Name: c.Name, Input: c.Args})
			}
			turns = append(turns, t)
		case "tool":
			res := llm.ToolResult{CallID: msg.ToolCallID, Content: msg.Text, IsError: msg.IsError}
			if n := len(turns); n > 0 && turns[n-1].Role == llm.RoleUser && len(turns[n-1].Results) > 0 {
				turns[n-1].Results = append(turns[n-1].Results, res)
			} else {
				turns = append(turns, llm.Turn{Role: llm.RoleUser, Results: []llm.ToolResult{res}})
			}
		default:
			return nil, fmt.Errorf("message %d: unknown role %q", i, msg.Role)
		}
	}
	return turns, nil
}

func toToolDefs(tools []runner.ToolDef) []llm.ToolDef {
	out := make([]llm.ToolDef, len(tools))
	for i, t := range tools {
		out[i] = llm.ToolDef{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}
	}
	return out
}

// fromReply converts the model's reply into the runner's assistant message.
func fromReply(r llm.ToolReply) runner.Message {
	msg := runner.Message{
		Role:      "assistant",
		Text:      r.Turn.Text,
		Stop:      stop(r.Stop),
		Raw:       r.Turn.Raw,
		RawFrom:   r.Turn.RawFrom,
		TokensIn:  r.Usage.InputTokens,
		TokensOut: r.Usage.OutputTokens,
	}
	for _, c := range r.Turn.Calls {
		msg.ToolCalls = append(msg.ToolCalls, runner.ToolCall{ID: c.ID, Name: c.Name, Args: c.Input})
	}
	return msg
}

// stop maps llm's stop reasons to the runner's; others pass through.
func stop(s string) string {
	switch s {
	case llm.StopEnd:
		return runner.StopEnd
	case llm.StopToolUse:
		return runner.StopToolUse
	case llm.StopMaxTokens:
		return runner.StopMaxTokens
	case llm.StopRefusal:
		return runner.StopRefusal
	default:
		return s
	}
}
