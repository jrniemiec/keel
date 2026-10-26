// Package config reads keel's config files: the keel config, set up once per
// machine, and task configs, one per task. The rest of keel gets ready
// values from it (runner.Task, mcphost.Server, llm.ProviderConfig).
//
// Both are JSON with comments. Keys left out keep their defaults; unknown
// keys are an error. Paths may start with "~/". Every default is in Default
// (the keel config) and in its Defaults (a task config); the example
// examples/config/keel.jsonc writes them all out, and a test keeps the two
// the same. See local/design-notes.md, "Config files and keel run".
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jrniemiec/llm"

	"github.com/jrniemiec/keel/internal/jsonc"
	"github.com/jrniemiec/keel/internal/klog"
	"github.com/jrniemiec/keel/mcphost"
	"github.com/jrniemiec/keel/runner"
)

// Default returns the keel config used for every key a file leaves out.
func Default() KeelConfig {
	return KeelConfig{
		DB:            filepath.Join(Home(), "data", "keel.db"),
		LogFile:       filepath.Join(Home(), "keel.log"),
		LogLevel:      "info",
		LogMaxMB:      50,
		LogBackups:    2,
		MaxToolOutput: 64 << 10,
		Defaults: TaskDefaults{
			Model:    "anthropic:claude-haiku-4-5-20251001",
			Thinking: false,
			System:   "You are running a task with tools. Use the tools to do the work. When the task is done, reply with a short summary.",
			MaxSteps: 20,
			Approval: runner.Approval{Read: runner.ApprovalAuto, Write: runner.ApprovalAsk, Destructive: runner.ApprovalAsk},
		},
		Providers: Providers{
			Anthropic: AnthropicConfig{MaxOutputTokens: 16000, Timeout: Duration(10 * time.Minute)},
			Ollama:    OllamaConfig{Host: "http://localhost:11434"},
		},
	}
}

// providers are the known providers, with the environment variable that
// holds each one's API key ("" = none needed). KEEL_<name> wins over <name>,
// e.g. KEEL_ANTHROPIC_API_KEY over ANTHROPIC_API_KEY, so keel can use its own
// key. Keys are read only from the environment, never from a config file.
var providers = map[string]string{
	"anthropic": "ANTHROPIC_API_KEY",
	"openai":    "OPENAI_API_KEY",
	"ollama":    "",
}

// KeelConfig is the keel config, by default ~/.keel/config/keel.jsonc.
type KeelConfig struct {
	DB            string                  `json:"db"`              // SQLite file with every task's step log
	LogFile       string                  `json:"log_file"`        // progress log, appended
	LogLevel      string                  `json:"log_level"`       // lowest level in the log file; keel run -l overrides it
	LogMaxMB      int                     `json:"log_max_mb"`      // the log file is rotated at this size
	LogBackups    int                     `json:"log_backups"`     // rotated log files kept
	MaxToolOutput int                     `json:"max_tool_output"` // bytes of one tool result the model sees; the rest is cut
	Defaults      TaskDefaults            `json:"defaults"`        // for keys a task config leaves out
	Servers       map[string]ServerConfig `json:"servers"`         // MCP servers by name, e.g. "fs"
	Providers     Providers               `json:"providers"`
}

// TaskDefaults are the values a task config gets for keys it leaves out.
type TaskDefaults struct {
	Model    string          `json:"model"`
	Thinking bool            `json:"thinking"`
	System   string          `json:"system"`
	MaxSteps int             `json:"max_steps"`
	Approval runner.Approval `json:"approval"`
}

// ServerConfig is an MCP server that keel starts as a child process.
type ServerConfig struct {
	Description string            `json:"description"` // what it offers, for keel servers; optional
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env"` // added to keel's environment
	// Hints correct the hints the server gives its tools; in order, later
	// ones win. E.g. {"tools": "read_*", "read_only": true}.
	Hints []mcphost.HintOverride `json:"hints"`
}

// Providers holds each provider's settings. API keys are not here: they
// come from the environment (see providers). OpenAI has no settings.
type Providers struct {
	Anthropic AnthropicConfig `json:"anthropic"`
	Ollama    OllamaConfig    `json:"ollama"`
}

// AnthropicConfig holds Anthropic's settings.
type AnthropicConfig struct {
	MaxOutputTokens int      `json:"max_output_tokens"` // tokens per reply, thinking included; a cut-off reply is asked again with double
	Timeout         Duration `json:"timeout"`           // wait for a reply, e.g. "10m"; replies aren't streamed
}

// OllamaConfig holds Ollama's settings.
type OllamaConfig struct {
	Host string `json:"host"`
}

// Duration is a time.Duration written as a string, e.g. "10m" or "90s".
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration: want a string such as \"10m\", got %s", b)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// TaskConfig is a task config: one file per task. It holds no machine paths
// or keys, so it can be shared.
type TaskConfig struct {
	Name     string   `json:"name"`      // the task ID, unless keel run --id is given
	Goal     string   `json:"goal"`      // what the model should do; its first message
	Model    string   `json:"model"`     // provider:model
	Thinking bool     `json:"thinking"`  // Anthropic: adaptive thinking; Ollama: think
	System   string   `json:"system"`    // system prompt
	Servers  []string `json:"servers"`   // from the keel config
	Allow    []string `json:"allow"`     // tools the model may use: names or path.Match patterns, e.g. "fs__read_*"
	MaxSteps int      `json:"max_steps"` // model calls
	// Approval says, per tool class (read, write, destructive), whether a
	// call runs ("auto") or keel run asks first ("ask"). Keys left out keep
	// the keel config's defaults.
	Approval runner.Approval `json:"approval"`
}

// Home returns keel's folder: $KEEL_HOME, or ~/.keel.
func Home() string {
	if h := os.Getenv("KEEL_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".keel"
	}
	return filepath.Join(home, ".keel")
}

// DefaultPath returns the keel config's default path, <home>/config/keel.jsonc.
func DefaultPath() string { return filepath.Join(Home(), "config", "keel.jsonc") }

// TasksDir returns the folder for task configs, <home>/config/tasks.
func TasksDir() string { return filepath.Join(Home(), "config", "tasks") }

// ModelsPath returns the models file's path, <home>/config/models.jsonc:
// prices and limits per model, made by keel models.
func ModelsPath() string { return filepath.Join(Home(), "config", "models.jsonc") }

// TaskConfigPath returns the path of the task config that keel run's
// argument names: a bare name such as "notes" means <TasksDir>/notes.jsonc;
// anything with a "/" or ending in ".jsonc" is a path, used as given.
func TaskConfigPath(arg string) string {
	if strings.Contains(arg, "/") || strings.HasSuffix(arg, ".jsonc") {
		return arg
	}
	return filepath.Join(TasksDir(), arg+".jsonc")
}

// LoadKeelConfig reads the keel config at path; "" means DefaultPath. A
// missing file at the default path gives the defaults; one given explicitly
// must exist.
func LoadKeelConfig(path string) (KeelConfig, error) {
	kc := Default()
	explicit := path != ""
	if !explicit {
		path = DefaultPath()
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && !explicit {
		return kc, nil
	}
	if err != nil {
		return KeelConfig{}, fmt.Errorf("keel config: %w", err)
	}
	if err := jsonc.Unmarshal(data, &kc); err != nil {
		return KeelConfig{}, fmt.Errorf("keel config %s: %w", path, err)
	}
	kc.expand()
	if err := kc.check(); err != nil {
		return KeelConfig{}, fmt.Errorf("keel config %s: %w", path, err)
	}
	return kc, nil
}

// expand replaces a leading "~" in every path.
func (kc *KeelConfig) expand() {
	kc.DB = ExpandHome(kc.DB)
	kc.LogFile = ExpandHome(kc.LogFile)
	for name, s := range kc.Servers {
		s.Command = ExpandHome(s.Command)
		s.Args = slices.Clone(s.Args)
		for i, a := range s.Args {
			s.Args[i] = ExpandHome(a)
		}
		kc.Servers[name] = s
	}
}

func (kc *KeelConfig) check() error {
	var errs []error
	if kc.DB == "" {
		errs = append(errs, errors.New("db is empty"))
	}
	if kc.LogFile == "" {
		errs = append(errs, errors.New("log_file is empty"))
	}
	for _, name := range slices.Sorted(maps.Keys(kc.Servers)) {
		s := kc.Servers[name]
		if s.Command == "" {
			errs = append(errs, fmt.Errorf("server %q: command is empty", name))
		}
		for i, o := range s.Hints {
			if _, err := path.Match(o.Tools, ""); err != nil || o.Tools == "" {
				errs = append(errs, fmt.Errorf("server %q: hints[%d]: tools %q: want a tool name or pattern", name, i, o.Tools))
			}
			if o.ReadOnly == nil && o.Destructive == nil && o.Idempotent == nil && o.OpenWorld == nil {
				errs = append(errs, fmt.Errorf("server %q: hints[%d]: no hint set", name, i))
			}
		}
	}
	if _, ok := klog.ParseLevel(kc.LogLevel); !ok {
		errs = append(errs, fmt.Errorf("log_level %q: want debug, info, warn or error", kc.LogLevel))
	}
	if kc.LogMaxMB <= 0 {
		errs = append(errs, errors.New("log_max_mb must be > 0"))
	}
	if kc.LogBackups < 0 {
		errs = append(errs, errors.New("log_backups must be >= 0"))
	}
	if err := kc.Defaults.Approval.Check(); err != nil {
		errs = append(errs, fmt.Errorf("defaults: %w", err))
	}
	if kc.MaxToolOutput <= 0 {
		errs = append(errs, errors.New("max_tool_output must be > 0"))
	}
	if kc.Providers.Anthropic.MaxOutputTokens <= 0 {
		errs = append(errs, errors.New("providers.anthropic.max_output_tokens must be > 0"))
	}
	if kc.Providers.Anthropic.Timeout <= 0 {
		errs = append(errs, errors.New("providers.anthropic.timeout must be > 0"))
	}
	return errors.Join(errs...)
}

// LoadTaskConfig reads the task config at path, which must exist. Keys it
// leaves out get the values in d, the keel config's defaults.
func LoadTaskConfig(path string, d TaskDefaults) (TaskConfig, error) {
	tc := TaskConfig{Model: d.Model, Thinking: d.Thinking, System: d.System, MaxSteps: d.MaxSteps, Approval: d.Approval}
	data, err := os.ReadFile(path)
	if err != nil {
		return TaskConfig{}, fmt.Errorf("task config: %w", err)
	}
	if err := jsonc.Unmarshal(data, &tc); err != nil {
		return TaskConfig{}, fmt.Errorf("task config %s: %w", path, err)
	}
	return tc, nil
}

// CheckTaskConfig checks tc against kc: its servers exist, its model names a
// known provider, and each allowed tool belongs to one of its servers. It
// reports all problems at once.
func CheckTaskConfig(kc KeelConfig, tc TaskConfig) error {
	var errs []error
	if tc.Name == "" {
		errs = append(errs, errors.New("name is empty"))
	}
	if tc.Goal == "" {
		errs = append(errs, errors.New("goal is empty"))
	}
	if tc.MaxSteps <= 0 {
		errs = append(errs, errors.New("max_steps must be > 0"))
	}
	if err := tc.Approval.Check(); err != nil {
		errs = append(errs, err)
	}
	if kind, name, ok := strings.Cut(tc.Model, ":"); !ok || name == "" {
		errs = append(errs, fmt.Errorf("model %q: want provider:model, e.g. anthropic:claude-haiku-4-5-20251001", tc.Model))
	} else if _, ok := providers[kind]; !ok {
		errs = append(errs, fmt.Errorf("model %q: provider %q: want anthropic, openai or ollama", tc.Model, kind))
	}
	if len(tc.Servers) == 0 {
		errs = append(errs, errors.New("servers is empty"))
	}
	for i, s := range tc.Servers {
		if _, ok := kc.Servers[s]; !ok {
			errs = append(errs, fmt.Errorf("server %q is not in the keel config", s))
		}
		if slices.Contains(tc.Servers[:i], s) {
			errs = append(errs, fmt.Errorf("server %q listed twice", s))
		}
	}
	if len(tc.Allow) == 0 {
		errs = append(errs, errors.New("allow is empty: the model would have no tools"))
	}
	for _, tool := range tc.Allow {
		server, name, ok := strings.Cut(tool, mcphost.Sep)
		if !ok || name == "" || !slices.Contains(tc.Servers, server) {
			errs = append(errs, fmt.Errorf("allow %q: want <server>%s<tool> with a server of this task", tool, mcphost.Sep))
		} else if _, err := path.Match(name, ""); err != nil {
			errs = append(errs, fmt.Errorf("allow %q: bad pattern", tool))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("task %s: %w", tc.Name, errors.Join(errs...))
	}
	return nil
}

// RunnerTask returns the task for the runner. Its ID is the task's name.
func (tc TaskConfig) RunnerTask() runner.Task {
	return runner.Task{
		ID:           tc.Name,
		Goal:         tc.Goal,
		AllowedTools: slices.Clone(tc.Allow),
		Model:        tc.Model,
		MaxSteps:     tc.MaxSteps,
		Approval:     tc.Approval,
		System:       tc.System,
		Thinking:     tc.Thinking,
	}
}

// MCPServers returns the named servers (all in kc; see CheckTaskConfig) for
// mcphost.Start.
func (kc KeelConfig) MCPServers(names []string) []mcphost.Server {
	out := make([]mcphost.Server, 0, len(names))
	for _, name := range names {
		s := kc.Servers[name]
		var env []string
		for _, k := range slices.Sorted(maps.Keys(s.Env)) {
			env = append(env, k+"="+s.Env[k])
		}
		out = append(out, mcphost.Server{Name: name, Command: s.Command, Args: s.Args, Env: env, Hints: s.Hints})
	}
	return out
}

// LLMConfigs returns the provider settings for tc's model, for
// model.NewLLM, with the API key the provider needs read from the
// environment. It fails when the key isn't set, so the task doesn't start
// without it.
func (kc KeelConfig) LLMConfigs(tc TaskConfig) (map[string]llm.ProviderConfig, error) {
	kind, _, _ := strings.Cut(tc.Model, ":")
	var cfg llm.ProviderConfig
	switch kind {
	case "anthropic":
		cfg.MaxOutputTokens = kc.Providers.Anthropic.MaxOutputTokens
		cfg.Timeout = time.Duration(kc.Providers.Anthropic.Timeout)
	case "ollama":
		cfg.Host = kc.Providers.Ollama.Host
	}

	if keyEnv := providers[kind]; keyEnv != "" {
		cfg.APIKey = strings.TrimSpace(os.Getenv("KEEL_" + keyEnv))
		if cfg.APIKey == "" {
			cfg.APIKey = strings.TrimSpace(os.Getenv(keyEnv))
		}
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("model %s: set %s (or KEEL_%s)", tc.Model, keyEnv, keyEnv)
		}
	}
	if tc.Thinking {
		switch kind {
		case "anthropic":
			cfg.Thinking = llm.ThinkingAdaptive
		case "ollama":
			cfg.Think = true
		}
	}
	return map[string]llm.ProviderConfig{kind: cfg}, nil
}

// ExpandHome replaces a leading "~" in path with the user's home folder.
func ExpandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}
