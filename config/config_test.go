package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
	d := TaskDefaults{Model: "ollama:m", Thinking: true, System: "s", MaxSteps: 7}
	tc, err := LoadTaskConfig(write(t, "notes.jsonc", `{"name": "t1", "goal": "g", "servers": ["fs"], "allow": ["fs__read_file"]}`), d)
	if err != nil {
		t.Fatal(err)
	}
	if tc.Model != d.Model || !tc.Thinking || tc.System != d.System || tc.MaxSteps != d.MaxSteps {
		t.Errorf("got %+v, want the defaults %+v", tc, d)
	}
	tc, err = LoadTaskConfig(write(t, "notes.jsonc", `{"name": "t1", "thinking": false, "max_steps": 3}`), d)
	if err != nil || tc.Thinking || tc.MaxSteps != 3 {
		t.Errorf("got %+v, %v; want the file's values over the defaults", tc, err)
	}
	tc.Allow = []string{"fs__read_file"}
	task := tc.RunnerTask()
	if task.ID != "t1" || task.Model != d.Model || task.AllowedTools[0] != "fs__read_file" {
		t.Errorf("RunnerTask = %+v", task)
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
	good := TaskConfig{Name: "t1", Goal: "g", Model: "anthropic:claude-x", MaxSteps: 5, Servers: []string{"fs"}, Allow: []string{"fs__read_file"}}
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

	got := kc
	got.Servers = nil
	if want := Default(); !reflect.DeepEqual(got, want) {
		t.Errorf("examples/config/keel.jsonc differs from Default():\n got %+v\nwant %+v", got, want)
	}
	d := kc.Defaults
	if tc.Model != d.Model || tc.Thinking != d.Thinking || tc.System != d.System {
		t.Errorf("notes.jsonc: model, thinking or system differ from the defaults")
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
