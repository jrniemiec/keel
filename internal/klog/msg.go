package klog

// Log messages. The msg field is the event type: always one of these fixed
// strings, with all values in attributes, so `grep 'msg="tool result"'` finds
// every entry of that kind.
const (
	// keel itself
	MsgStarted = "keel started"
	MsgCrash   = "debug crash" // keel run --crash-at exits the process

	// a task (runner); entries carry task=<id>
	MsgTaskStart    = "task start"
	MsgTaskResume   = "task resume"
	MsgModelChanged = "model changed" // resumed with another model than the last reply's
	MsgTaskDone     = "task done"
	MsgTaskFailed   = "task failed"
	MsgTaskStopped  = "task stopped" // ctx cancelled; stays running, resumed later
	MsgTaskBlocked  = "task blocked" // stopped by its environment (tools changed, server down); not failed

	// a step; entries also carry seq and kind
	MsgModelReply  = "model reply"
	MsgToolCall    = "tool call"    // intent saved, about to run
	MsgToolResult  = "tool result"  // WARN when the call failed
	MsgToolSkipped = "tool skipped" // has a result from before a restart
	MsgToolDenied  = "tool denied"  // not in the task's allowed tools
	MsgToolRetry   = "tool retry"   // server down; read-only or idempotent, so called again
	MsgIntentRerun = "intent rerun" // intent without a result from before a restart; safe, so called again

	// the task's tools (runner); see local/design-notes.md, "Tool snapshot"
	MsgToolSnapshot = "tool snapshot" // WARN: saved late, for a task from before snapshots
	MsgToolRemoved  = "tool removed"  // in the snapshot, no longer listed; dropped
	MsgToolAdded    = "tool added"    // allowed by a pattern, not in the snapshot; ignored

	// DEBUG payloads
	MsgModelText = "model text"
	MsgToolArgs  = "tool args"
	MsgToolOut   = "tool output"

	// model adapter
	MsgMaxTokensRetry = "max tokens retry"

	// MCP servers (mcphost); entries carry server=<name>
	MsgMCPUp          = "mcp server up"
	MsgMCPDown        = "mcp server down"    // a call failed: the server died
	MsgMCPRestart     = "mcp server restart" // a down server, before its next call
	MsgMCPStderr      = "mcp stderr"         // a line the server wrote to stderr
	MsgMCPTools       = "mcp tools"          // tools=<count> listed
	MsgMCPToolSkipped = "mcp tool skipped"   // not offered to the model
	MsgMCPHintUnused  = "mcp hint unused"    // a hint override matches no tool of its server
)
