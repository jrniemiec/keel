// Command keel runs agent tasks with a durable step log. A task is described
// by a task config; keel run starts it, or resumes it if it was interrupted.
// Watch progress with kstat in another window.
//
//	keel run notes          # ~/.keel/config/tasks/notes.jsonc
//	keel run --id n2 notes  # same task config, a new task
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/jrniemiec/keel/config"
	"github.com/jrniemiec/keel/internal/klog"
	"github.com/jrniemiec/keel/mcphost"
	"github.com/jrniemiec/keel/model"
	"github.com/jrniemiec/keel/runner"
	"github.com/jrniemiec/keel/store"
)

var version = "dev" // set by the Makefile

// helpText is printed by keel -h, keel --help and keel run -h. It is the only
// help, so keep it next to the flags in run and in step with them.
const helpText = `keel runs agent tasks with a durable step log.

Usage:
  keel run [flags] <task>

Commands:
  run    Run a task, or resume it from its last saved step after a crash
         or Ctrl-C. A done task prints its saved answer. The task's
         settings must not change between runs; for new settings, start
         a new task (another name, or --id). <task> is a task name, read
         from $KEEL_HOME/config/tasks/<task>.jsonc, or the path of a task
         config (it has a "/" or ends in .jsonc).

Flags for run:
  -c, --config <file>      use this keel config (servers, db, log) instead of $KEEL_HOME/config/keel.jsonc (KEEL_HOME default ~/.keel)
  -d, --db <file>          save steps in this database instead of the one in the keel config; e.g. for tests
  -i, --id <id>            task ID instead of the name in the task config; a new ID starts a new task, a used one resumes it
  -l, --log-level <level>  lowest level written to the log file: debug (adds model text, tool args and output), info, warn, error; overrides log_level in the keel config
  -v, --verbose            also print the log to the terminal, including debug entries
  -h, --help               show this help

Files ($KEEL_HOME, default ~/.keel):
  config/keel.jsonc           keel config: servers, db, log; one per machine
  config/tasks/<task>.jsonc   task configs, one per task
  data/keel.db                the database
  keel.log                    the log file
Commented examples are in examples/config; install them with cp -r examples/config ~/.keel/

Examples:
  keel run notes                              run the task, or resume it
  keel run --id n2 notes                      same task config, a new task
  keel run examples/config/tasks/notes.jsonc  a task config given by path
  keel run -d /tmp/test.db -v notes           experiment in a separate database

Log file: log_file in the keel config (default ~/.keel/keel.log); follow it with tail -f.
Watch all tasks: kstat -w.
API keys are read from the environment, never from a config file:
  Anthropic models   ANTHROPIC_API_KEY (KEEL_ANTHROPIC_API_KEY wins if set)
  OpenAI models      OPENAI_API_KEY (KEEL_OPENAI_API_KEY wins if set)
  Ollama models      no key
`

const usageLine = "usage: keel run [flags] <task>\nrun 'keel -h' for help"

// usageError is a mistake on the command line: exit 2 with the usage line.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func main() {
	err := dispatch(os.Args[1:])
	var ue usageError
	switch {
	case errors.As(err, &ue):
		fmt.Fprintf(os.Stderr, "keel: %s\n%s\n", ue.msg, usageLine)
		os.Exit(2)
	case err != nil:
		fmt.Fprintln(os.Stderr, "keel:", err)
		os.Exit(1)
	}
}

func dispatch(args []string) error {
	if len(args) == 0 {
		return usageError{"no command"}
	}
	switch args[0] {
	case "-h", "-help", "--help":
		fmt.Print(helpText)
		return nil
	case "run":
		return run(args[1:])
	default:
		return usageError{fmt.Sprintf("unknown command %q", args[0])}
	}
}

// runFlags holds keel run's flags.
type runFlags struct {
	config, db, id, logLevel string
	verbose, help            bool
}

// newRunFlags defines keel run's flags, each with a short and a long name.
// They are described in helpText.
func newRunFlags() (*flag.FlagSet, *runFlags) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are reported by main, help by helpText
	f := &runFlags{}
	for _, name := range []string{"c", "config"} {
		fs.StringVar(&f.config, name, "", "")
	}
	for _, name := range []string{"d", "db"} {
		fs.StringVar(&f.db, name, "", "")
	}
	for _, name := range []string{"i", "id"} {
		fs.StringVar(&f.id, name, "", "")
	}
	for _, name := range []string{"l", "log-level"} {
		fs.StringVar(&f.logLevel, name, "", "")
	}
	for _, name := range []string{"v", "verbose"} {
		fs.BoolVar(&f.verbose, name, false, "")
	}
	for _, name := range []string{"h", "help"} {
		fs.BoolVar(&f.help, name, false, "")
	}
	fs.IntVar(&crashAt, "crash-at", 0, "") // not in the help: exit before the Nth model or tool call, for tests
	return fs, f
}

// run is "keel run": it starts or resumes the task in a task config.
func run(args []string) error {
	flags, f := newRunFlags()

	// Flags may come before or after the task config.
	var paths []string
	for {
		if err := flags.Parse(args); err != nil {
			return usageError{err.Error()}
		}
		if flags.NArg() == 0 {
			break
		}
		paths = append(paths, flags.Arg(0))
		args = flags.Args()[1:]
	}
	if f.help {
		fmt.Print(helpText)
		return nil
	}
	switch len(paths) {
	case 0:
		return usageError{"no task config given"}
	case 1:
	default:
		return usageError{fmt.Sprintf("one task config at a time, got %d", len(paths))}
	}
	taskPath := config.TaskConfigPath(paths[0])

	kc, err := config.LoadKeelConfig(f.config)
	if err != nil {
		return err
	}
	tc, err := config.LoadTaskConfig(taskPath, kc.Defaults)
	if errors.Is(err, fs.ErrNotExist) && taskPath != paths[0] {
		return fmt.Errorf("no task %q: %s doesn't exist", paths[0], taskPath)
	}
	if err != nil {
		return err
	}
	if err := config.CheckTaskConfig(kc, tc); err != nil {
		return err
	}
	if f.db != "" {
		kc.DB = f.db
	}
	llmConfigs, err := kc.LLMConfigs(tc)
	if err != nil {
		return err
	}
	task := tc.RunnerTask()
	if f.id != "" {
		task.ID = f.id
	}

	logLevel := kc.LogLevel
	if f.logLevel != "" {
		logLevel = f.logLevel
	}
	level, ok := klog.ParseLevel(logLevel)
	if !ok {
		return usageError{fmt.Sprintf("--log-level %q: want debug, info, warn or error", logLevel)}
	}
	klog.Init(kc.LogFile, level, f.verbose, kc.LogMaxMB, kc.LogBackups)
	klog.Info(klog.MsgStarted, "version", version, "db", kc.DB, "task", task.ID, "model", task.Model)

	if err := os.MkdirAll(filepath.Dir(kc.DB), 0o755); err != nil {
		return err
	}
	db, err := store.OpenSQLite(kc.DB)
	if err != nil {
		return err
	}
	defer db.Close()

	// Ctrl-C stops the task; it stays running and resumes on the next run.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	host, err := mcphost.Start(ctx, kc.MCPServers(tc.Servers), kc.MaxToolOutput)
	if err != nil {
		return err
	}
	defer host.Close()

	r := runner.Runner{
		Model: crashModel{model.NewLLM(llmConfigs, tc.System)},
		Tools: crashTools{host},
		Store: db,
	}
	res, err := r.Run(ctx, task)
	if errors.Is(err, runner.ErrTaskChanged) {
		return fmt.Errorf("%w; to change them, start a new task (another name, or --id)", err)
	}
	if err != nil {
		return err
	}
	fmt.Printf("answer: %s (%d model calls)\n", res.Text, res.Steps)
	return nil
}
