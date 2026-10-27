package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jrniemiec/keel/models"
	"github.com/jrniemiec/keel/runner"
)

func write(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadKeelConfigDefaults(t *testing.T) {
	t.Setenv("KEEL_HOME", t.TempDir()) // no config/keel.jsonc there
	kc, err := LoadKeelConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(Home(), "data", "keel.db"); kc.DB != want {
		t.Errorf("DB = %q, want %q", kc.DB, want)
	}
	if _, err := LoadKeelConfig(filepath.Join(Home(), "missing.jsonc")); err == nil {
		t.Error("an explicit missing file: want an error")
	}
}

func TestLoadKeelConfig(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	path := write(t, "keel.jsonc", `{
		// comments and trailing commas are fine
		"db": "~/k/keel.db",
		"servers": {
			"fs": {"command": "~/bin/fs", "args": ["~/data", "-x", "a~b"], "env": {"B": "2", "A": "1"}},
		},
	}`)
	kc, err := LoadKeelConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "k", "keel.db"); kc.DB != want {
		t.Errorf("DB = %q, want %q", kc.DB, want)
	}
	if !strings.HasSuffix(kc.LogFile, "keel.log") {
		t.Errorf("LogFile = %q, want the default", kc.LogFile)
	}
	s := kc.MCPServers([]string{"fs"})[0]
	if s.Command != filepath.Join(home, "bin", "fs") || s.Args[0] != filepath.Join(home, "data") || s.Args[2] != "a~b" {
		t.Errorf("server = %+v, want ~ expanded at the start only", s)
	}
	if strings.Join(s.Env, " ") != "A=1 B=2" {
		t.Errorf("Env = %v, want sorted KEY=value", s.Env)
	}
}

func TestLoadKeelConfigErrors(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"unknown key", `{"sever": {}}`, `unknown field "sever"`},
		{"no command", `{"servers": {"fs": {}}}`, `server "fs": command is empty`},
		{"unknown provider", `{"providers": {"gemini": {}}}`, `unknown field "gemini"`},
		{"bad log level", `{"log_level": "loud"}`, `log_level "loud"`},
		{"bad timeout", `{"providers": {"anthropic": {"timeout": "soon"}}}`, `duration "soon"`},
		{"timeout as a number", `{"providers": {"anthropic": {"timeout": 600}}}`, `want a string such as "10m"`},
		{"no tool output", `{"max_tool_output": 0}`, "max_tool_output must be > 0"},
		{"no keys in config", `{"providers": {"anthropic": {"api_key_env": "X"}}}`, `unknown field "api_key_env"`},
		{"hint bad pattern", `{"servers": {"fs": {"command": "x", "hints": [{"tools": "read_[", "read_only": true}]}}}`, `hints[0]: tools "read_["`},
		{"hint no tools", `{"servers": {"fs": {"command": "x", "hints": [{"read_only": true}]}}}`, `hints[0]: tools ""`},
		{"hint none set", `{"servers": {"fs": {"command": "x", "hints": [{"tools": "read_*"}]}}}`, "hints[0]: no hint set"},
		{"hint unknown key", `{"servers": {"fs": {"command": "x", "hints": [{"tools": "read_*", "readonly": true}]}}}`, `unknown field "readonly"`},
		{"bad default approval", `{"defaults": {"approval": {"destructive": "never"}}}`, `defaults: approval destructive "never"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadKeelConfig(write(t, "keel.jsonc", tt.src))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestLoadTaskConfig(t *testing.T) {
	d := TaskDefaults{Model: "ollama:m", Thinking: true, System: "s", MaxSteps: 7, MaxUSD: 1, Approval: Default().Defaults.Approval}
	tc, err := LoadTaskConfig(write(t, "notes.jsonc", `{"name": "t1", "goal": "g", "servers": ["fs"], "allow": ["fs__read_file"]}`), d)
	if err != nil {
		t.Fatal(err)
	}
	if tc.Model != d.Model || !tc.Thinking || tc.System != d.System || tc.MaxSteps != d.MaxSteps || tc.MaxUSD != d.MaxUSD {
		t.Errorf("got %+v, want the defaults %+v", tc, d)
	}
	tc, err = LoadTaskConfig(write(t, "notes.jsonc", `{"name": "t1", "thinking": false, "max_steps": 3, "max_usd": 2.5}`), d)
	if err != nil || tc.Thinking || tc.MaxSteps != 3 || tc.MaxUSD != 2.5 {
		t.Errorf("got %+v, %v; want the file's values over the defaults", tc, err)
	}
	if tc.Approval != d.Approval {
		t.Errorf("Approval = %+v, want the defaults", tc.Approval)
	}
	tc.Allow = []string{"fs__read_file"}
	task := tc.RunnerTask()
	if task.ID != "t1" || task.Model != d.Model || task.AllowedTools[0] != "fs__read_file" || task.MaxUSD != 2.5 {
		t.Errorf("RunnerTask = %+v", task)
	}

	tc, err = LoadTaskConfig(write(t, "notes.jsonc", `{"name": "t1", "approval": {"destructive": "auto"}}`), d)
	if want := (runner.Approval{Read: "auto", Write: "ask", Destructive: "auto"}); err != nil || tc.Approval != want {
		t.Errorf("Approval = %+v, %v; want %+v: the file's key over the defaults, the rest kept", tc.Approval, err, want)
	}

	if _, err := LoadTaskConfig(filepath.Join(t.TempDir(), "missing.jsonc"), d); err == nil {
		t.Error("a missing task config: want an error")
	}
	_, err = LoadTaskConfig(write(t, "notes.jsonc", `{"name": "t1", "modle": "x"}`), d)
	if err == nil || !strings.Contains(err.Error(), `unknown field "modle"`) {
		t.Errorf("err = %v, want unknown field", err)
	}
}

func TestCheckTaskConfig(t *testing.T) {
	kc := KeelConfig{Servers: map[string]ServerConfig{"fs": {Command: "fs"}, "web": {Command: "web"}}}
	good := TaskConfig{Name: "t1", Goal: "g", Model: "anthropic:claude-x", MaxSteps: 5, Servers: []string{"fs"}, Allow: []string{"fs__read_file"},
		Approval: Default().Defaults.Approval}
	if err := CheckTaskConfig(kc, good); err != nil {
		t.Fatalf("good task: %v", err)
	}

	tests := []struct {
		name   string
		change func(*TaskConfig)
		want   string
	}{
		{"no name", func(tc *TaskConfig) { tc.Name = "" }, "name is empty"},
		{"no goal", func(tc *TaskConfig) { tc.Goal = "" }, "goal is empty"},
		{"max steps", func(tc *TaskConfig) { tc.MaxSteps = 0 }, "max_steps"},
		{"negative max_usd", func(tc *TaskConfig) { tc.MaxUSD = -1 }, "max_usd must be >= 0"},
		{"model without provider", func(tc *TaskConfig) { tc.Model = "claude" }, "want provider:model"},
		{"unknown provider", func(tc *TaskConfig) { tc.Model = "gemini:pro" }, `provider "gemini"`},
		{"no servers", func(tc *TaskConfig) { tc.Servers = nil; tc.Allow = nil }, "servers is empty"},
		{"unknown server", func(tc *TaskConfig) { tc.Servers = []string{"fs", "db"} }, `server "db" is not in the keel config`},
		{"server twice", func(tc *TaskConfig) { tc.Servers = []string{"fs", "fs"} }, "listed twice"},
		{"no allow", func(tc *TaskConfig) { tc.Allow = nil }, "allow is empty"},
		{"tool of another server", func(tc *TaskConfig) { tc.Allow = []string{"web__fetch"} }, `allow "web__fetch"`},
		{"tool without server", func(tc *TaskConfig) { tc.Allow = []string{"read_file"} }, `allow "read_file"`},
		{"bad pattern", func(tc *TaskConfig) { tc.Allow = []string{"fs__[read"} }, "bad pattern"},
		{"wildcard server", func(tc *TaskConfig) { tc.Allow = []string{"*__read_file"} }, `allow "*__read_file"`},
		{"bare wildcard", func(tc *TaskConfig) { tc.Allow = []string{"*"} }, `allow "*"`},
		{"bad approval", func(tc *TaskConfig) { tc.Approval.Write = "yes" }, `approval write "yes": want auto or ask`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := good
			tt.change(&tc)
			err := CheckTaskConfig(kc, tc)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestLLMConfigs(t *testing.T) {
	t.Setenv("KEEL_ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	tc := TaskConfig{Model: "anthropic:claude-x", Thinking: true}
	if _, err := (KeelConfig{}).LLMConfigs(tc); err == nil || !strings.Contains(err.Error(), "set ANTHROPIC_API_KEY (or KEEL_ANTHROPIC_API_KEY)") {
		t.Errorf("err = %v, want missing key", err)
	}

	t.Setenv("ANTHROPIC_API_KEY", "std")
	cfgs, err := (KeelConfig{}).LLMConfigs(tc)
	if err != nil {
		t.Fatal(err)
	}
	if c := cfgs["anthropic"]; c.APIKey != "std" || c.Thinking != "adaptive" {
		t.Errorf("anthropic = %+v", c)
	}

	t.Setenv("KEEL_ANTHROPIC_API_KEY", "keel")
	cfgs, err = (KeelConfig{}).LLMConfigs(tc)
	if err != nil || cfgs["anthropic"].APIKey != "keel" {
		t.Errorf("key = %q, %v; want KEEL_ANTHROPIC_API_KEY to win", cfgs["anthropic"].APIKey, err)
	}

	cfgs, err = (KeelConfig{}).LLMConfigs(TaskConfig{Model: "ollama:qwen3.5:9b", Thinking: true})
	if err != nil || !cfgs["ollama"].Think {
		t.Errorf("ollama = %+v, %v; want Think and no key needed", cfgs["ollama"], err)
	}
}

// The examples describe the file formats, so they must stay valid, and the
// example keel config must show keel's defaults (all but its servers).
func TestExamples(t *testing.T) {
	t.Setenv("KEEL_HOME", "") // ~/.keel, as in the example's paths
	kc, err := LoadKeelConfig("../examples/config/keel.jsonc")
	if err != nil {
		t.Fatal(err)
	}
	tc, err := LoadTaskConfig("../examples/config/tasks/notes.jsonc", kc.Defaults)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckTaskConfig(kc, tc); err != nil {
		t.Error(err)
	}

	if n := len(kc.Servers["fs"].Hints); n != 6 {
		t.Errorf("fs hints = %d, want the example's 6", n)
	}

	got := kc
	got.Servers = nil
	if want := Default(); !reflect.DeepEqual(got, want) {
		t.Errorf("examples/config/keel.jsonc differs from Default():\n got %+v\nwant %+v", got, want)
	}
	d := kc.Defaults
	if tc.Model != d.Model || tc.Thinking != d.Thinking || tc.System != d.System || tc.Approval != d.Approval {
		t.Errorf("notes.jsonc: model, thinking, system or approval differ from the defaults")
	}
}

func TestTaskConfigPath(t *testing.T) {
	t.Setenv("KEEL_HOME", "/k")
	tests := map[string]string{
		"notes":            "/k/config/tasks/notes.jsonc",
		"notes.jsonc":      "notes.jsonc",
		"./notes":          "./notes",
		"examples/x.jsonc": "examples/x.jsonc",
		"/abs/notes.jsonc": "/abs/notes.jsonc",
	}
	for arg, want := range tests {
		if got := TaskConfigPath(arg); got != want {
			t.Errorf("TaskConfigPath(%q) = %q, want %q", arg, got, want)
		}
	}
}

func TestCheckModel(t *testing.T) {
	ms := models.Models{
		"anthropic:paid":  {InputPrice: 1, OutputPrice: 5, ToolCalling: true, Thinking: true},
		"ollama:free":     {ToolCalling: true},
		"ollama:no-tools": {},
	}
	tc := TaskConfig{Name: "t1", Model: "anthropic:paid", MaxUSD: 1}
	if _, err := CheckModel(tc, ms, "models.jsonc"); err != nil {
		t.Errorf("paid model with max_usd: %v", err)
	}
	free := TaskConfig{Name: "t1", Model: "ollama:free"}
	if _, err := CheckModel(free, ms, "models.jsonc"); err != nil {
		t.Errorf("free model without max_usd: %v", err)
	}
	for _, tt := range []struct {
		name    string
		change  func(*TaskConfig)
		wantErr string
	}{
		{"not in the file", func(tc *TaskConfig) { tc.Model = "openai:new" }, "model openai:new is not in models.jsonc; add it with make models"},
		{"paid without max_usd", func(tc *TaskConfig) { tc.MaxUSD = 0 }, "model anthropic:paid costs money: set max_usd > 0"},
		{"no tool calling", func(tc *TaskConfig) { tc.Model = "ollama:no-tools" }, "model ollama:no-tools has no tool calling"},
		{"no thinking", func(tc *TaskConfig) { tc.Model, tc.Thinking = "ollama:free", true }, "model ollama:free has no thinking"},
	} {
		bad := tc
		tt.change(&bad)
		if _, err := CheckModel(bad, ms, "models.jsonc"); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.wantErr)
		}
	}
}
