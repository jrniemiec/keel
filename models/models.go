// Package models reads and writes keel's models file,
// ~/.keel/config/models.jsonc: prices and limits per model, keyed by
// provider:model. keel models makes the file from LiteLLM's model list
// (litellm.go); the user reviews it and saves it, keel never writes it.
package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jrniemiec/keel/internal/jsonc"
)

// Model is one model's entry in the models file.
type Model struct {
	InputPrice      Price  `json:"input_price_per_1m_tokens"`
	OutputPrice     Price  `json:"output_price_per_1m_tokens"`
	MaxOutputTokens Tokens `json:"max_output_tokens,omitempty"` // 0: unknown
	ContextTokens   Tokens `json:"context_tokens,omitempty"`    // 0: unknown
	ToolCalling     bool   `json:"tool_calling"`
	Thinking        bool   `json:"thinking"`
	RetiresOn       string `json:"retires_on,omitempty"` // YYYY-MM-DD; "": not announced
}

// Cost returns what a reply with these token counts costs, in USD.
func (m Model) Cost(tokensIn, tokensOut int) float64 {
	return (float64(tokensIn)*float64(m.InputPrice) + float64(tokensOut)*float64(m.OutputPrice)) / 1e6
}

// Paid reports whether the model costs money.
func (m Model) Paid() bool { return m.InputPrice > 0 || m.OutputPrice > 0 }

// Models maps provider:model, e.g. "anthropic:claude-sonnet-5", to its entry.
type Models map[string]Model

// Price is USD per 1M tokens. In the file it is a string, "$3.00", so the
// unit can't be mistaken.
type Price float64

// String returns the price as in the file: "$3.00", "$0.075".
func (p Price) String() string {
	s := strconv.FormatFloat(float64(p), 'f', -1, 64)
	dot := strings.IndexByte(s, '.')
	switch {
	case dot < 0:
		s += ".00"
	case len(s)-dot-1 < 2:
		s += "0"
	}
	return "$" + s
}

func (p Price) MarshalJSON() ([]byte, error) { return json.Marshal(p.String()) }

func (p *Price) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("price %s: want a string such as \"$3.00\"", b)
	}
	num, ok := strings.CutPrefix(s, "$")
	v, err := strconv.ParseFloat(num, 64)
	if !ok || err != nil || v < 0 || math.IsInf(v, 0) || math.IsNaN(v) {
		return fmt.Errorf("price %q: want $ and a number, such as \"$3.00\"", s)
	}
	*p = Price(v)
	return nil
}

// Tokens is a token count. In the file it is "64K" (×1,000), "1M"
// (×1,000,000), or a plain number when it isn't round.
type Tokens int

// String returns the count as in the file, without quotes: 64K, 1M, 1047576.
func (t Tokens) String() string {
	switch {
	case t != 0 && t%1_000_000 == 0:
		return strconv.Itoa(int(t/1_000_000)) + "M"
	case t != 0 && t%1_000 == 0:
		return strconv.Itoa(int(t/1_000)) + "K"
	default:
		return strconv.Itoa(int(t))
	}
}

func (t Tokens) MarshalJSON() ([]byte, error) {
	s := t.String()
	if _, err := strconv.Atoi(s); err == nil {
		return []byte(s), nil
	}
	return json.Marshal(s)
}

func (t *Tokens) UnmarshalJSON(b []byte) error {
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		if n <= 0 {
			return fmt.Errorf("token count %d: want more than 0", n)
		}
		*t = Tokens(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("token count %s: want \"64K\", \"1M\" or a number", b)
	}
	mult, num := 0, s
	if k, ok := strings.CutSuffix(s, "K"); ok {
		mult, num = 1_000, k
	} else if m, ok := strings.CutSuffix(s, "M"); ok {
		mult, num = 1_000_000, m
	}
	n, err := strconv.Atoi(num)
	if mult == 0 || err != nil || n <= 0 {
		return fmt.Errorf("token count %q: want \"64K\", \"1M\" or a number", s)
	}
	*t = Tokens(n * mult)
	return nil
}

// Load reads the models file at path.
func Load(path string) (Models, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("models file: %w", err)
	}
	var m Models
	if err := jsonc.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("models file %s: %w", path, err)
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(m)) {
		if p, model, ok := strings.Cut(name, ":"); !ok || p == "" || model == "" {
			errs = append(errs, fmt.Errorf("%q: want provider:model, e.g. anthropic:claude-sonnet-5", name))
		}
		if d := m[name].RetiresOn; d != "" {
			if _, err := time.Parse(time.DateOnly, d); err != nil {
				errs = append(errs, fmt.Errorf("%s: retires_on %q: want YYYY-MM-DD", name, d))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("models file %s: %w", path, err)
	}
	return m, nil
}

// Write writes m as a models file: the header lines as // comments, then
// one model per block, in name order, values aligned.
func Write(w io.Writer, m Models, header []string) error {
	var b strings.Builder
	for _, h := range header {
		b.WriteString(strings.TrimRight("// "+h, " ") + "\n")
	}
	b.WriteString("{\n")
	names := slices.Sorted(maps.Keys(m))
	for i, name := range names {
		fmt.Fprintf(&b, "  %q: {\n", name)
		fields := m[name].fields()
		for j, f := range fields {
			comma := ","
			if j == len(fields)-1 {
				comma = ""
			}
			fmt.Fprintf(&b, "    %-29s %s%s\n", strconv.Quote(f.name)+":", f.value, comma)
		}
		if i < len(names)-1 {
			b.WriteString("  },\n")
		} else {
			b.WriteString("  }\n")
		}
	}
	b.WriteString("}\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// fieldOrder is the order of a model's fields in the file.
var fieldOrder = []string{
	"input_price_per_1m_tokens", "output_price_per_1m_tokens", "max_output_tokens",
	"context_tokens", "tool_calling", "thinking", "retires_on",
}

// field is one key and its JSON value, as written in the file.
type field struct{ name, value string }

// fields returns m's fields in fieldOrder, leaving out unknown ones.
func (m Model) fields() []field {
	js := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	fs := []field{
		{"input_price_per_1m_tokens", js(m.InputPrice)},
		{"output_price_per_1m_tokens", js(m.OutputPrice)},
	}
	if m.MaxOutputTokens != 0 {
		fs = append(fs, field{"max_output_tokens", js(m.MaxOutputTokens)})
	}
	if m.ContextTokens != 0 {
		fs = append(fs, field{"context_tokens", js(m.ContextTokens)})
	}
	fs = append(fs, field{"tool_calling", js(m.ToolCalling)}, field{"thinking", js(m.Thinking)})
	if m.RetiresOn != "" {
		fs = append(fs, field{"retires_on", js(m.RetiresOn)})
	}
	return fs
}

// Select returns the models whose names match one of patterns (path.Match
// globs, e.g. "anthropic:*"); no patterns selects all. unmatchedPatterns are
// the patterns that matched no model.
func Select(m Models, patterns []string) (selected Models, unmatchedPatterns []string, err error) {
	for _, p := range patterns {
		if _, err := path.Match(p, ""); err != nil {
			return nil, nil, fmt.Errorf("pattern %q: %w", p, err)
		}
	}
	if len(patterns) == 0 {
		return m, nil, nil
	}
	selected = Models{}
	for _, p := range patterns {
		matched := false
		for name, model := range m {
			if ok, _ := path.Match(p, name); ok {
				selected[name] = model
				matched = true
			}
		}
		if !matched {
			unmatchedPatterns = append(unmatchedPatterns, p)
		}
	}
	return selected, unmatchedPatterns, nil
}

// Diff returns the differences from old to new, one line each, in name
// order: "+ name" added, "- name" removed, "~ name  field  old → new"
// changed. A field missing on one side shows as "-".
func Diff(old, new Models) []string {
	names := slices.Sorted(maps.Keys(old))
	for name := range new {
		if _, ok := old[name]; !ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	var lines []string
	for _, name := range names {
		o, inOld := old[name]
		n, inNew := new[name]
		switch {
		case !inOld:
			lines = append(lines, "+ "+name)
		case !inNew:
			lines = append(lines, "- "+name)
		default:
			lines = append(lines, diffFields(name, o.fields(), n.fields())...)
		}
	}
	return lines
}

// diffFields returns the "~" lines for one model's changed fields.
func diffFields(name string, old, new []field) []string {
	value := func(fs []field, key string) string {
		for _, f := range fs {
			if f.name == key {
				return f.value
			}
		}
		return "-"
	}
	var lines []string
	for _, key := range fieldOrder {
		if o, n := value(old, key), value(new, key); o != n {
			lines = append(lines, fmt.Sprintf("~ %s  %s  %s → %s", name, key, o, n))
		}
	}
	return lines
}
