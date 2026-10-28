# keel

keel runs agent tasks with a durable step log. A task is a goal, a model and
a set of MCP tools. keel calls the model, runs the tools it asks for, and
saves every step in SQLite before going on. If keel crashes or you press
Ctrl-C, `keel run` again resumes the task from its last saved step.

- **Resume after a crash.** Each tool call is saved as an intent before it
  runs and as a result after. On resume, a call that may have run already is
  repeated only if its tool is read-only or idempotent; otherwise keel asks.
- **Approvals.** Per tool class (read, write, destructive), a call runs
  automatically or keel asks first: run it, run it and later calls to this
  tool, don't run it, or don't run it and tell the model why.
- **Limits.** Each task has a step limit and a USD budget, priced from a
  models file. When one is used up, the task waits; raise it and run again.
- **Several providers.** Anthropic, OpenAI and Ollama. The model may change
  between runs of a task, e.g. to continue on another provider when one is
  down.
- **Context management.** Long tool results are cut, and old ones are cleared
  from what the model sees as the history fills the context window. The
  database keeps everything whole.

keel is tested on macOS.

## Install

Go 1.26 or later:

```sh
go install github.com/jrniemiec/keel/cmd/keel@latest
go install github.com/jrniemiec/keel/cmd/kstat@latest
```

Or from a clone: `make build` puts both binaries in `bin/`.

## Quick start

The example task reads a file with an MCP filesystem server, counts its words
and lines, and writes a report next to it.

```sh
# The example configs, into ~/.keel/config
git clone https://github.com/jrniemiec/keel && cd keel
mkdir -p ~/.keel && cp -r examples/config ~/.keel/

# The MCP server the example uses, and a folder for it. The keel config
# expects the server in ~/go/bin; change "command" there if yours is elsewhere.
go install github.com/mark3labs/mcp-filesystem-server@latest
mkdir -p ~/.keel/demo && echo "hello keel" > ~/.keel/demo/notes.txt

# Run the task
export ANTHROPIC_API_KEY=...
keel run notes
```

keel shows each model call and tool call as it goes. Before `write_file` it
asks for approval. The answer goes to stdout; progress goes to stderr.

Then:

```sh
keel tasks            # all tasks, latest first
keel steps notes      # what the task said and did, step by step
kstat -w              # live view of all tasks, in another window
```

## Configuration

All files live in `$KEEL_HOME` (default `~/.keel`). The examples in
[`examples/config`](examples/config) are commented line by line.

| file | what |
|---|---|
| `config/keel.jsonc` | MCP servers, database, log, and defaults for tasks; one per machine |
| `config/tasks/<task>.jsonc` | one task: goal, model, servers, allowed tools, limits, approvals |
| `config/models.jsonc` | prices and limits per model; made by `keel models` |
| `data/keel.db` | the step log |
| `keel.log` | the log file; follow it with `tail -f` |

A task may call only the tools in its `allow` list. Tool names are
`<server>__<tool>`; `keel servers --tools` lists them with their hints.
MCP servers often mark their tools wrongly (e.g. every tool destructive);
`hints` in the keel config corrects them.

API keys are read from the environment, never from a config file:
`ANTHROPIC_API_KEY` and `OPENAI_API_KEY`, or `KEEL_ANTHROPIC_API_KEY` and
`KEEL_OPENAI_API_KEY`, which win if set. Ollama needs no key.

To refresh prices and limits: `keel models --diff` shows what changed;
`keel models > ~/.keel/config/models.jsonc` saves the new file. It is made
from [LiteLLM's model list](https://github.com/BerriAI/litellm) and the
models installed in Ollama.

## Commands

```
keel run [flags] <task>        run a task, or resume it
keel tasks [flags]             list tasks
keel steps [flags] <task-id>   show a task's steps
keel servers [flags]           list MCP servers, and with --tools their tools
keel models [flags] [<pattern>...]   print a models file
kstat [-w] [-db file]          show tasks and their progress
```

`keel --help` has the details.

## When a task waits

A task stops in the `waiting` state, not `failed`, when it needs you: a
limit was reached, the context window is full, a tool server died, the
tools changed since the task started, or a call that may have run can't be
repeated safely. `keel tasks -s waiting` shows why. Fix what it names, then
`keel run` the task again.

## License

[MIT](LICENSE)
