package klog

// Log messages. The msg field is the event type: always one of these fixed
// strings, with all values in attributes, so `grep 'msg="tool result"'` finds
// every entry of that kind.
const (
	// keel itself
	MsgStarted = "keel started"
	MsgCrash   = "demo crash" // cmd/demo: -crash-at exits the process

	// a task (runner); entries carry task=<id>
	MsgTaskStart   = "task start"
	MsgTaskResume  = "task resume"
	MsgTaskDone    = "task done"
	MsgTaskFailed  = "task failed"
	MsgTaskStopped = "task stopped" // ctx cancelled; stays running, resumed later

	// a step; entries also carry seq and kind
	MsgModelReply  = "model reply"
	MsgToolCall    = "tool call"    // intent saved, about to run
	MsgToolResult  = "tool result"  // WARN when the call failed
	MsgToolSkipped = "tool skipped" // has a result from before a restart
	MsgToolDenied  = "tool denied"  // not in the task's allowed tools

	// DEBUG payloads
	MsgModelText = "model text"
	MsgToolArgs  = "tool args"
	MsgToolOut   = "tool output"

	// model adapter
	MsgMaxTokensRetry = "max tokens retry"
)
