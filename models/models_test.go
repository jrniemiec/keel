package models

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestFromLiteLLM(t *testing.T) {
	data, err := Fetch(t.Context(), "testdata/litellm.json")
	if err != nil {
		t.Fatal(err)
	}
	m, pricingPages, err := FromLiteLLM(data)
	if err != nil {
		t.Fatal(err)
	}
	want := Models{
		"anthropic:claude-sonnet-4-5": {InputPrice: 3, OutputPrice: 15, MaxOutputTokens: 64_000, ContextTokens: 200_000,
			ToolCalling: true, Thinking: true, RetiresOn: "2026-11-30"},
		"anthropic:claude-haiku-4-5": {InputPrice: 1, OutputPrice: 5, MaxOutputTokens: 64_000, ContextTokens: 200_000, ToolCalling: true},
		"anthropic:claude-opus-5": {InputPrice: 5, OutputPrice: 25, MaxOutputTokens: 128_000, ContextTokens: 1_000_000,
			ToolCalling: true, Thinking: true},
		"openai:gpt-4.1":           {InputPrice: 2, OutputPrice: 8, MaxOutputTokens: 32_768, ContextTokens: 1_047_576, ToolCalling: true},
		"openai:gpt-5-nano":        {InputPrice: 0.05, OutputPrice: 0.4, ToolCalling: true, Thinking: true},
		"openai:gpt-3.5-turbo-16k": {InputPrice: 3, OutputPrice: 4, MaxOutputTokens: 4096, ContextTokens: 16_385},
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("models:\n%v\nwant:\n%v", m, want)
	}
	wantPages := map[string]string{
		"anthropic": "https://platform.claude.com/docs/en/about-claude/pricing", // cited twice, overview once
		"openai":    "https://developers.openai.com/api/docs/pricing",
	}
	if !maps.Equal(pricingPages, wantPages) {
		t.Errorf("pricing pages = %q, want %q", pricingPages, wantPages)
	}
}

func TestWriteLoad(t *testing.T) {
	m := Models{
		"openai:gpt-4.1": {InputPrice: 2, OutputPrice: 8, MaxOutputTokens: 32_768, ContextTokens: 1_047_576, ToolCalling: true},
		"anthropic:claude-sonnet-4-5": {InputPrice: 3, OutputPrice: 15, MaxOutputTokens: 64_000, ContextTokens: 1_000_000,
			ToolCalling: true, Thinking: true, RetiresOn: "2026-11-30"},
		"openai:gpt-5-nano": {InputPrice: 0.05, OutputPrice: 0.4},
	}
	var b bytes.Buffer
	if err := Write(&b, m, []string{"made by a test", ""}); err != nil {
		t.Fatal(err)
	}
	want := `// made by a test
//
{
  "anthropic:claude-sonnet-4-5": {
    "input_price_per_1m_tokens":  "$3.00",
    "output_price_per_1m_tokens": "$15.00",
    "max_output_tokens":          "64K",
    "context_tokens":             "1M",
    "tool_calling":               true,
    "thinking":                   true,
    "retires_on":                 "2026-11-30"
  },
  "openai:gpt-4.1": {
    "input_price_per_1m_tokens":  "$2.00",
    "output_price_per_1m_tokens": "$8.00",
    "max_output_tokens":          32768,
    "context_tokens":             1047576,
    "tool_calling":               true,
    "thinking":                   false
  },
  "openai:gpt-5-nano": {
    "input_price_per_1m_tokens":  "$0.05",
    "output_price_per_1m_tokens": "$0.40",
    "tool_calling":               false,
    "thinking":                   false
  }
}
`
	if b.String() != want {
		t.Errorf("file:\n%s\nwant:\n%s", b.String(), want)
	}

	path := filepath.Join(t.TempDir(), "models.jsonc")
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, m) {
		t.Errorf("loaded:\n%v\nwant:\n%v", got, m)
	}
}

func TestLoadErrors(t *testing.T) {
	for _, tc := range []struct{ name, file, wantErr string }{
		{"no provider", `{"claude-x": {"input_price_per_1m_tokens": "$1", "output_price_per_1m_tokens": "$2"}}`, "want provider:model"},
		{"bad date", `{"a:b": {"input_price_per_1m_tokens": "$1", "output_price_per_1m_tokens": "$2", "retires_on": "30.11.2026"}}`, "want YYYY-MM-DD"},
		{"unknown field", `{"a:b": {"input_price": "$1"}}`, "unknown field"},
		{"bare price", `{"a:b": {"input_price_per_1m_tokens": 3}}`, `want a string such as "$3.00"`},
	} {
		path := filepath.Join(t.TempDir(), "models.jsonc")
		if err := os.WriteFile(path, []byte(tc.file), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestPrice(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Price
	}{{`"$3.00"`, 3}, {`"$3"`, 3}, {`"$0.075"`, 0.075}, {`"$0"`, 0}} {
		var p Price
		if err := json.Unmarshal([]byte(tc.in), &p); err != nil || p != tc.want {
			t.Errorf("%s: got %v, %v; want %v", tc.in, p, err, tc.want)
		}
	}
	for _, in := range []string{`3`, `"3"`, `"€3"`, `"3¢"`, `"$"`, `"$-1"`, `"$ 3"`, `"$Inf"`} {
		var p Price
		if err := json.Unmarshal([]byte(in), &p); err == nil {
			t.Errorf("%s: no error, got %v", in, p)
		}
	}
	for p, want := range map[Price]string{3: "$3.00", 12.5: "$12.50", 0.075: "$0.075", 0: "$0.00"} {
		if got := p.String(); got != want {
			t.Errorf("Price(%v) = %s, want %s", float64(p), got, want)
		}
	}
	if got := perMillion(4.9999999999999996e-08); got != 0.05 {
		t.Errorf("perMillion = %v, want 0.05", got)
	}
}

func TestTokens(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Tokens
	}{{`"64K"`, 64_000}, {`"1M"`, 1_000_000}, {`1047576`, 1_047_576}, {`"1050K"`, 1_050_000}} {
		var n Tokens
		if err := json.Unmarshal([]byte(tc.in), &n); err != nil || n != tc.want {
			t.Errorf("%s: got %v, %v; want %v", tc.in, n, err, tc.want)
		}
	}
	for _, in := range []string{`"64k"`, `"64"`, `"K"`, `"1.5M"`, `"64KM"`, `0`, `-5`, `"0K"`, `true`} {
		var n Tokens
		if err := json.Unmarshal([]byte(in), &n); err == nil {
			t.Errorf("%s: no error, got %d", in, n)
		}
	}
}

func TestSelect(t *testing.T) {
	m := Models{"anthropic:claude-opus-5": {}, "anthropic:claude-haiku-4-5": {}, "openai:gpt-5": {}}
	got, unmatched, err := Select(m, []string{"anthropic:*opus*", "openai:*", "ollama:*"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"anthropic:claude-opus-5", "openai:gpt-5"}; !slices.Equal(slices.Sorted(maps.Keys(got)), want) {
		t.Errorf("selected %q, want %q", slices.Sorted(maps.Keys(got)), want)
	}
	if !slices.Equal(unmatched, []string{"ollama:*"}) {
		t.Errorf("unmatched = %q", unmatched)
	}
	if got, _, _ := Select(m, nil); len(got) != 3 {
		t.Errorf("no patterns: %d models, want 3", len(got))
	}
	if _, _, err := Select(m, []string{"["}); err == nil {
		t.Error("bad pattern: no error")
	}
}

func TestDiff(t *testing.T) {
	old := Models{
		"a:same":    {InputPrice: 1, OutputPrice: 2},
		"a:gone":    {InputPrice: 1, OutputPrice: 2},
		"a:changed": {InputPrice: 3, OutputPrice: 15, ContextTokens: 200_000},
	}
	new := Models{
		"a:same":    {InputPrice: 1, OutputPrice: 2},
		"a:added":   {InputPrice: 1, OutputPrice: 2},
		"a:changed": {InputPrice: 2.5, OutputPrice: 15, ContextTokens: 1_000_000, RetiresOn: "2027-01-31"},
	}
	want := []string{
		"+ a:added",
		`~ a:changed  input_price_per_1m_tokens  "$3.00" → "$2.50"`,
		`~ a:changed  context_tokens  "200K" → "1M"`,
		`~ a:changed  retires_on  - → "2027-01-31"`,
		"- a:gone",
	}
	if got := Diff(old, new); !slices.Equal(got, want) {
		t.Errorf("diff:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := Diff(old, old); len(got) != 0 {
		t.Errorf("same models: %q", got)
	}
}
