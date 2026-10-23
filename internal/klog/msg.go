package klog

// Log messages. The msg field is the event type: always one of these fixed
// strings, with all values in attributes, so `grep 'msg="tool result"'` finds
// every entry of that kind.
const (
	// keel itself
	MsgStarted = "keel started"
	MsgCrash   = "debug crash" // keel run --crash-at exits the process

	// a task (runner); entries carry task=<id>
	MsgTaskStart   = "task start"
	MsgTaskResume  = "task resume"
	MsgTaskDone    = "task done"
	MsgTaskFailed  = "task failed"
	MsgTaskStopped = "task stopped" // ctx cancelled; stays running, resumed later
	MsgTaskBlocked = "task blocked" // not resumed: tools changed since its snapshot; stays as it was

	// a step; entries also carry seq and kind
	MsgModelReply  = "model reply"
	MsgToolCall    = "tool call"    // intent saved, about to run
	MsgToolResult  = "tool result"  // WARN when the call failed
	MsgToolSkipped = "tool skipped" // has a result from before a restart
	MsgToolDenied  = "tool denied"  // not in the task's allowed tools

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
	MsgMCPTools       = "mcp tools"        // tools=<count> listed
	MsgMCPToolSkipped = "mcp tool skipped" // not offered to the model
)
