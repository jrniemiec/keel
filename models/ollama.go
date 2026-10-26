package models

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// FromOllama returns the models installed in the Ollama server at host,
// e.g. "http://localhost:11434", keyed "ollama:<name>". They are free; the
// rest comes from /api/show: tool calling and thinking from the model's
// capabilities, context_tokens from the most context it supports. Ollama
// has no separate output limit, so max_output_tokens is left out.
func FromOllama(ctx context.Context, host string) (Models, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	host = strings.TrimRight(host, "/")
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := ollamaCall(ctx, http.MethodGet, host+"/api/tags", nil, &tags); err != nil {
		return nil, err
	}
	m := Models{}
	for _, t := range tags.Models {
		var show struct {
			Capabilities []string       `json:"capabilities"`
			ModelInfo    map[string]any `json:"model_info"`
		}
		body, _ := json.Marshal(map[string]string{"model": t.Name})
		if err := ollamaCall(ctx, http.MethodPost, host+"/api/show", body, &show); err != nil {
			return nil, fmt.Errorf("%s: %w", t.Name, err)
		}
		model := Model{
			ToolCalling: slices.Contains(show.Capabilities, "tools"),
			Thinking:    slices.Contains(show.Capabilities, "thinking"),
		}
		for k, v := range show.ModelInfo { // e.g. "llama.context_length": 131072
			if n, ok := v.(float64); ok && strings.HasSuffix(k, ".context_length") {
				model.ContextTokens = Tokens(n)
			}
		}
		m["ollama:"+t.Name] = model
	}
	return m, nil
}

// ollamaCall sends a request to Ollama and decodes the JSON reply into out.
func ollamaCall(ctx context.Context, method, url string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	return nil
}
