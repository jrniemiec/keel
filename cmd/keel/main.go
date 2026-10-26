// Command keel runs agent tasks with a durable step log. A task is described
// by a task config; keel run starts it, or resumes it if it was interrupted.
// keel tasks and keel steps show the saved tasks; watch progress with kstat
// in another window.
//
//	keel run notes          # ~/.keel/config/tasks/notes.jsonc
//	keel run --id n2 notes  # same task config, a new task
//	keel steps notes        # what the task did, step by step
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
	"time"

	"github.com/mattn/go-isatty"

	"github.com/jrniemiec/keel/config"
	"github.com/jrniemiec/keel/internal/klog"
	"github.com/jrniemiec/keel/internal/tasklock"
	"github.com/jrniemiec/keel/mcphost"
	"github.com/jrniemiec/keel/model"
	"github.com/jrniemiec/keel/runner"
	"github.com/jrniemiec/keel/store"
)

var version = "dev" // set by the Makefile

// helpText is printed by keel -h, keel --help and each command's -h. It is
// the only help, so keep it in step with the commands' flags.
const helpText = `keel runs agent tasks with a durable step log.

Usage:
  keel run [flags] <task>
  keel tasks [flags]
  keel steps [flags] <task-id>
  keel servers [flags]
  keel models [flags] [<pattern>...]

Commands:
  run    Run a task, or resume it from its last saved step after a crash
         or Ctrl-C. A done task prints its saved answer. Between runs
         only model and thinking may change (e.g. another provider when
         one is down); for other new settings, start a new task (another
         name, or --id). <task> is a task name, read
         from $KEEL_HOME/config/tasks/<task>.jsonc, or the path of a task
         config (it has a "/" or ends in .jsonc).
         Before a call the task's approval setting says "ask" for (by
         default writes and destructive calls), it shows the call and asks:
           y  run it
           a  run it, and later calls to this tool in this run
           n  don't run it (Enter too); the model is told
           m  don't run it, and type a message for the model
           v  show the arguments in full, then ask again
         A call that may have run before keel stopped (a crash) and isn't
         safe to repeat is asked about too. Without a terminal (stdin not
         a terminal), the task waits; keel run in a terminal asks.
         One keel process per task: a second keel run of a running task
         stops with an error naming the other process.
  tasks  List the tasks in the database, latest first, once. For live
         progress, use kstat -w.
  steps  Show a task's steps in order: model replies, tool calls and
         results. <task-id> is a task ID in the database (keel tasks),
         not a task config.
  servers
         List the MCP servers in the keel config, with the description
         given there. With --tools, start them
         and list their tools: the names to use in a task's allow list,
         and the hints keel uses, after the keel config's overrides.
  models Print a models file (prices and limits per model) made from
         LiteLLM's model list (Anthropic and OpenAI chat models) and the
         models installed in Ollama (host in the keel config; left out,
         with a note, when it can't be reached). Check it, then save it:
         keel models > $KEEL_HOME/config/models.jsonc.
         <pattern> keeps only matching models, e.g. 'anthropic:*'; a
         pattern matching no model is an error.

Flags for run:
  -c, --config <file>      use this keel config (servers, db, log) instead of $KEEL_HOME/config/keel.jsonc (KEEL_HOME default ~/.keel)
  -d, --db <file>          save steps in this database instead of the one in the keel config; e.g. for tests
  -i, --id <id>            task ID instead of the name in the task config; a new ID starts a new task, a used one resumes it
  -l, --log-level <level>  lowest level written to the log file: debug (adds model text, tool args and output), info, warn, error; overrides log_level in the keel config
  -v, --verbose            also print the log to the terminal, including debug entries
  -h, --help               show this help

Flags for tasks and steps:
  -c, --config <file>      use this keel config to find the database
  -d, --db <file>          read this database instead of the one in the keel config
  -s, --status <status>    tasks only: show only running, waiting, failed or done tasks
      --full               tasks: NOTE whole, on one line; steps: text whole, below its row, and tool args
  -h, --help               show this help

Flags for servers:
  -c, --config <file>      use this keel config instead of $KEEL_HOME/config/keel.jsonc
      --tools              start the servers and list their tools with hints

Flags for models:
  -c, --config <file>      use this keel config (Ollama host) instead of $KEEL_HOME/config/keel.jsonc
      --source <url|file>  read the model list from here instead of LiteLLM's on GitHub
      --diff               show what differs from $KEEL_HOME/config/models.jsonc instead of printing the file

Files ($KEEL_HOME, default ~/.keel):
  config/keel.jsonc           keel config: servers, db, log; one per machine
  config/tasks/<task>.jsonc   task configs, one per task
  config/models.jsonc         prices and limits per model; made by keel models
  data/keel.db                the database
  data/locks/<task>.lock      held while a task runs; needs a local disk
  keel.log                    the log file
Commented examples are in examples/config; install them with cp -r examples/config ~/.keel/

Examples:
  keel run notes                              run the task, or resume it
  keel run --id n2 notes                      same task config, a new task
  keel run examples/config/tasks/notes.jsonc  a task config given by path
  keel run -d /tmp/test.db -v notes           experiment in a separate database
  keel tasks -s waiting                       tasks waiting for you, and why
  keel steps --full notes                     everything the task said and did
  keel servers --tools                        tool names for a task's allow list
  keel models --diff 'anthropic:*'            what changed in Anthropic's prices

Log file: log_file in the keel config (default ~/.keel/keel.log); follow it with tail -f.
Watch all tasks: kstat -w.
API keys are read from the environment, never from a config file:
  Anthropic models   ANTHROPIC_API_KEY (KEEL_ANTHROPIC_API_KEY wins if set)
  OpenAI models      OPENAI_API_KEY (KEEL_OPENAI_API_KEY wins if set)
  Ollama models      no key
`

const usageLine = "usage: keel run [flags] <task> | keel tasks [flags] | keel steps [flags] <task-id> | keel servers [flags] | keel models [flags] [<pattern>...]\nrun 'keel -h' for help"

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
	case "tasks":
		return tasks(args[1:], os.Stdout)
	case "steps":
		return steps(args[1:], os.Stdout)
	case "servers":
		return servers(args[1:], os.Stdout)
	case "models":
		return modelsCmd(args[1:], os.Stdout, os.Stderr, time.Now())
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
	paths, err := parseArgs(flags, args)
	if err != nil {
		return err
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

	// One process per task: a second keel run of it stops here, before
	// starting servers.
	unlock, err := tasklock.Lock(filepath.Join(filepath.Dir(kc.DB), "locks"), task.ID)
	if err != nil {
		return err
	}
	defer unlock()

	// Ctrl-C stops the task; it stays running and resumes on the next run.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	host, err := mcphost.Start(ctx, kc.MCPServers(tc.Servers), kc.MaxToolOutput)
	if err != nil {
		return err
	}
	defer host.Close()

	r := runner.Runner{
		Model:    crashModel{model.NewLLM(llmConfigs, tc.System)},
		Tools:    crashTools{host},
		Store:    db,
		Progress: os.Stderr,
	}
	if isatty.IsTerminal(os.Stdin.Fd()) {
		r.Approver = newTerminalApprover(os.Stdin, os.Stderr)
	} // else a call that needs approval makes the task wait
	res, err := r.Run(ctx, task)
	if errors.Is(err, runner.ErrTaskChanged) {
		return fmt.Errorf("%w; to change them, start a new task (another name, or --id)", err)
	}
	if errors.Is(err, runner.ErrApprovalNeeded) {
		return fmt.Errorf("%w; run keel run %s in a terminal to answer", err, paths[0])
	}
	if err != nil {
		return err
	}
	fmt.Printf("answer: %s (%d model calls)\n", res.Text, res.Steps)
	return nil
}
