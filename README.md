# keel

keel is a durable runtime for long-running LLM agent tasks. A task is a goal, a
model and a set of MCP tools. keel calls the model, runs the tools it asks for,
and appends every step to a log in a database before going on. Writes pause for
approval. If keel crashes or you press Ctrl-C, `keel run` again resumes the task
from its last saved step.

- **Crash-consistent resume.** Each tool call is appended to the log as an intent
  before it runs and as a result after, so a crash leaves at most one call whose
  outcome is unknown. On resume that call is repeated only if its tool is
  read-only or idempotent; otherwise keel asks.
- **Human-in-the-loop approvals.** Per tool class (read, write, destructive), a
  call runs automatically or keel asks first: run it, run it and later calls to
  this tool, don't run it, or don't run it and tell the model why.
- **Least privilege per task.** A task may call only the tools in its
  allow-list; a call to any other tool is refused before it runs. Tools are
  namespaced `<server>__<tool>`.
- **Budgets.** Each task has a step limit and a USD ceiling, priced per model from
  a models file. Every reply's cost is saved with it. When a limit is used up the
  task waits; raise it and run again.
- **Several providers.** Anthropic, OpenAI and Ollama. The model may change
  between runs of one task, e.g. to continue on another provider when one is down.
- **Context-window management.** Long tool results are cut, and old ones are
  cleared from what the model sees as the history fills the window. The database
  keeps everything whole.

keel is tested on macOS. It is pre-1.0: the CLI and the config files may still
change.

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

keel shows each model call and tool call as it goes, with the cost so far and the
size of the history against the model's context window. Before `write_file` it
asks for approval. The answer goes to stdout; progress goes to stderr.

Then:

```sh
keel tasks            # all tasks, latest first
keel steps notes      # what the task said and did, step by step
kstat -w              # live view of all tasks, in another window
```

## How it works

Each action is a step in one append-only table, appended before the action it
describes:

```
model_reply → intent → tool_result → model_reply → approval_asked → approved → …
```

`intent` is appended before a tool call, `tool_result` after it. An interrupted
task therefore holds at most one call of unknown outcome, identified in the log
as an intent with no result. Two guarantees hold at the storage layer:

- `PRAGMA synchronous = FULL` — an appended step is on disk before the call it
  describes is made.
- `UNIQUE INDEX one_result ON steps(task_id, call_id) WHERE kind = 'tool_result'`
  — a call has at most one recorded result, enforced by the schema rather than by
  the code that writes it.

On resume, a call of unknown outcome is resolved by its tool's idempotency class,
read from the tool snapshot saved at task start:

| Tool | Action |
|---|---|
| read-only or idempotent | called again, reusing the saved intent |
| any other | task stops: `waiting`, reason `outcome unknown`; asked on the next run |

keel has no undo mechanism; repeat and ask are the two available resolutions.
The guarantee is at most one recorded result per call, not exactly-once
execution.

Server-declared hints are often wrong: `mcp-filesystem-server` reports
`read_file` as destructive. The keel config overrides hints per server, and
each tool definition carries both the server's declared hints and the effective
ones. The tool snapshot compares the declared set, so editing an override is not
tool drift.

Policy — approvals, budgets, the allow-list — is the control plane; model and
tool calls are the data plane. Each call is evaluated by the control plane before
execution, and the decision is appended next to the step it permits. A server
that dies mid-call is resolved by the same idempotency class: the retry is
decided first and the server restarted only in order to serve it. A safe tool is
called again; any other leaves the intent without a result and stops the task.

## Configuration

All files live in `$KEEL_HOME` (default `~/.keel`). The examples in
[`examples/config`](examples/config) are commented line by line.

| file | what |
|---|---|
| `config/keel.jsonc` | MCP servers, hint overrides, database, log, and defaults for tasks; one per machine |
| `config/tasks/<task>.jsonc` | one task: goal, model, servers, allowed tools, limits, approvals |
| `config/models.jsonc` | prices and limits per model; made by `keel models` |
| `data/keel.db` | the step log |
| `data/locks/<task>.lock` | held while a task runs; needs a local disk |
| `keel.log` | the log file; follow it with `tail -f` |

A task may call only the tools in its `allow` list. Tool names are
`<server>__<tool>`; `keel servers --tools` lists them with their hints.
MCP servers often mark their tools wrongly (e.g. every tool destructive);
`hints` in the keel config corrects them.

API keys are read from the environment, not from a config file:
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
keel version                   print the version
kstat [-w] [-db file]          show tasks and their progress
```

`keel --help` has the details.

## Cost and limits

Prices are not held in code. `keel models` writes a models file from LiteLLM's
model list and the models Ollama has installed; keel checks it on every run for
the model the task is about to use. The model must be listed, must support tool
calling, and if it costs money the task must set `max_usd` — an unknown model is
an error, not free.

Each reply's cost is computed from its token counts and saved with the step, so
the task's total is a sum of what was actually spent and a later price change
does not rewrite it. `keel tasks` and `kstat -w` show it per task; the progress
line shows it per call:

```
model  anthropic:claude-haiku-4-5: fs__read_file  $0.004 (total $0.031 of $2.00)  history 84K of 200K (42%)
```

`max_steps`, `max_usd`, `max_tool_result_tokens` and
`clear_tool_results_at_percent` are the settings you may change between runs of
one task: raise the limit, run again, and the task continues.

## Watching a task

Three views:

| | |
|---|---|
| `keel steps <task>` | what the task said and did, in order — model replies, tool calls with their arguments, results, approvals and rejections |
| `keel tasks` / `kstat -w` | one row per task: status, what it is doing now, steps, model and tool calls, tool errors, tokens, cost, last error |
| `keel.log` | everything keel does, as structured entries with fixed message keys, rotated; `tail -f` or `grep` it |

## When a task waits

A task stops in the `waiting` state, not `failed`, when it needs you: a limit was
reached, the context window is full, a tool server died, the tools changed since
the task started, or a call that may have run can't be repeated safely.
`keel tasks -s waiting` shows why. Fix what it names, then `keel run` the task
again.

A task whose process crashed or was interrupted stays `running`; it resumes on
the next `keel run`.

## Scope

v1 is single user, single node: one process, SQLite, stdio MCP servers, approvals
in the terminal. keel runs one agent per task, does not evaluate or score what a
task produced, and has no automatic undo. Planned, in roughly this order: a
supervisor so tasks run unattended, cron and file-change triggers, OpenTelemetry
tracing, remote MCP over Streamable HTTP with auth, prompt-injection defense, and
multi-node storage behind the existing `Store` interface.

## Development

```sh
make test    # go test ./...
make vet
make lint    # golangci-lint
make build   # bin/keel, bin/kstat
```

The test suite covers:

- **Resume decisions**, driven by replaying saved step sequences — a pending
  read-only call runs again and leaves one intent in the log; a pending write
  blocks with nothing saved; a call that already has a result is skipped.
- **Approvals** — each answer, "always" for the rest of a run, and a decision
  applying to the right ask of the right call.
- **Budgets** — cost summed across a resume, stopping at the limit, raising it and
  continuing.
- **Context window** — the cut at a line boundary, clearing down to the target
  percent, which messages are always kept, the marker reused on resume.
- **MCP** — hint overrides applied in order, a server crash retried once for a
  safe tool and not for an unsafe one, stderr captured and split into lines.
  These run against a test binary acting as a real MCP server.

## License

[MIT](LICENSE)
