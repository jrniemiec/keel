package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestModels(t *testing.T) {
	const list = "../../models/testdata/litellm.json"
	now := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	home := t.TempDir()
	t.Setenv("KEEL_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	// An Ollama that can't be reached: noted, and the rest still printed.
	keelConf := `{"providers": {"ollama": {"host": "http://127.0.0.1:1"}}}`
	if err := os.WriteFile(filepath.Join(home, "config", "keel.jsonc"), []byte(keelConf), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if err := modelsCmd([]string{"--source", list, "anthropic:*"}, &out, &errOut, now); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"// keel models file: made by keel models on 2026-10-24 from\n//   " + list + "\n",
		"//   https://platform.claude.com/docs/en/about-claude/pricing\n{\n",
		`"anthropic:claude-opus-5": {`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output has no %q:\n%s", want, out.String())
		}
	}
	if !strings.Contains(errOut.String(), "keel: no Ollama models: ") {
		t.Errorf("stderr = %q, want a note on Ollama", errOut.String())
	}
	if strings.Contains(out.String(), "openai:") {
		t.Errorf("pattern anthropic:* kept an OpenAI model:\n%s", out.String())
	}

	if err := modelsCmd([]string{"--source", list, "anthropic:*", "ollama:*"}, &bytes.Buffer{}, &bytes.Buffer{}, now); err == nil ||
		!strings.Contains(err.Error(), "no model matches ollama:*") {
		t.Errorf("unmatched pattern: err = %v", err)
	}

	// --diff against a saved file: one price changed, one model removed.
	saved := strings.Replace(out.String(), `"$25.00"`, `"$30.00"`, 1)
	saved = strings.Replace(saved, `"anthropic:claude-haiku-4-5"`, `"anthropic:claude-old"`, 1)
	if err := os.WriteFile(filepath.Join(home, "config", "models.jsonc"), []byte(saved), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := modelsCmd([]string{"--source", list, "--diff", "anthropic:*"}, &out, &bytes.Buffer{}, now); err != nil {
		t.Fatal(err)
	}
	want := "+ anthropic:claude-haiku-4-5\n- anthropic:claude-old\n" +
		`~ anthropic:claude-opus-5  output_price_per_1m_tokens  "$30.00" → "$25.00"` + "\n"
	if out.String() != want {
		t.Errorf("diff:\n%s\nwant:\n%s", out.String(), want)
	}
}
