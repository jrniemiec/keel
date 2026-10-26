package models

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

// LiteLLMURL is LiteLLM's model list: prices and limits for most providers'
// models, kept up to date by the LiteLLM project. Providers' own APIs return
// no prices.
const LiteLLMURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// maxListSize caps the download; the list is about 1.5 MB.
const maxListSize = 50 << 20

// Fetch returns the model list at source: an http(s) URL, or a file path
// (for tests or offline use).
func Fetch(ctx context.Context, source string) ([]byte, error) {
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
		return os.ReadFile(source)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", source, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxListSize+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if len(data) > maxListSize {
		return nil, fmt.Errorf("%s: larger than %d MB", source, maxListSize>>20)
	}
	return data, nil
}

// liteLLMEntry holds the fields keel uses from one LiteLLM entry.
type liteLLMEntry struct {
	Provider        string   `json:"litellm_provider"`
	Mode            string   `json:"mode"`
	InputCost       *float64 `json:"input_cost_per_token"`
	OutputCost      *float64 `json:"output_cost_per_token"`
	MaxInput        int      `json:"max_input_tokens"`
	MaxOutput       int      `json:"max_output_tokens"`
	FunctionCalling bool     `json:"supports_function_calling"`
	Reasoning       bool     `json:"supports_reasoning"`
	Deprecation     string   `json:"deprecation_date"`
	Source          string   `json:"source"`
}

// providers are the LiteLLM providers keel keeps. Ollama models are free,
// so they need no entry.
var providers = []string{"anthropic", "openai"}

// FromLiteLLM converts LiteLLM's model list to Models. It keeps chat models
// (keel uses Chat Completions) of providers with both prices set, leaving
// out fine-tunes ("ft:") and names with "/". pricingPages maps a provider
// to the pricing page its kept entries cite most.
func FromLiteLLM(data []byte) (m Models, pricingPages map[string]string, err error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("model list: %w", err)
	}
	m = Models{}
	cited := map[string]map[string]int{} // provider → source URL → entries
	for name, msg := range raw {
		// Entries of other providers may have other shapes (e.g. sample_spec
		// documents the fields with strings), so check the provider first.
		var head struct {
			Provider string `json:"litellm_provider"`
		}
		if json.Unmarshal(msg, &head) != nil || !slices.Contains(providers, head.Provider) {
			continue
		}
		var e liteLLMEntry
		if err := json.Unmarshal(msg, &e); err != nil {
			return nil, nil, fmt.Errorf("model list: %s: %w", name, err)
		}
		if e.Mode != "chat" || e.InputCost == nil || e.OutputCost == nil ||
			strings.HasPrefix(name, "ft:") || strings.Contains(name, "/") {
			continue
		}
		m[e.Provider+":"+name] = Model{
			InputPrice:      perMillion(*e.InputCost),
			OutputPrice:     perMillion(*e.OutputCost),
			MaxOutputTokens: Tokens(e.MaxOutput),
			ContextTokens:   Tokens(e.MaxInput),
			ToolCalling:     e.FunctionCalling,
			Thinking:        e.Reasoning,
			RetiresOn:       e.Deprecation,
		}
		if strings.HasPrefix(e.Source, "https://") {
			if cited[e.Provider] == nil {
				cited[e.Provider] = map[string]int{}
			}
			cited[e.Provider][e.Source]++
		}
	}
	pricingPages = map[string]string{}
	for p, counts := range cited {
		top := ""
		for src, n := range counts {
			if top == "" || n > counts[top] || n == counts[top] && src < top {
				top = src
			}
		}
		pricingPages[p] = top
	}
	return m, pricingPages, nil
}

// perMillion converts a price per token to a Price (per 1M tokens), rounded
// to millionths of a dollar so 3e-06 gives $3.00, not $2.9999999999999996.
func perMillion(perToken float64) Price {
	return Price(math.Round(perToken*1e6*1e6) / 1e6)
}
