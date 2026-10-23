package store

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jrniemiec/keel/runner"
	"sync"
	"time"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

type SQLite struct {
	conn *sqlite.Conn
	mu   sync.Mutex
}

func OpenSQLite(path string) (*SQLite, error) {

	conn, err := sqlite.OpenConn(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	// Per connection, and outside ExecuteScript's transaction, where it may not apply.
	if err := sqlitex.ExecuteTransient(conn, "PRAGMA synchronous = FULL;", nil); err != nil {
		conn.Close()
		return nil, fmt.Errorf("set synchronous %s: %w", path, err)
	}
	if err := sqlitex.ExecuteScript(conn, schema, nil); err != nil {
		conn.Close()
		return nil, fmt.Errorf("init schema %s: %w", path, err)
	}
	return &SQLite{conn: conn}, nil
}

func (sl *SQLite) Close() error { return sl.conn.Close() }

// StartTask implements runner.Store: it adds the task's row, or on resume
// marks it running again. The spec of a resumed task is kept as first saved;
// task must match it, else runner.ErrTaskChanged.
func (sl *SQLite) StartTask(ctx context.Context, task runner.Task) error {
	spec, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("encode task: %w", err)
	}

	sl.mu.Lock()
	defer sl.mu.Unlock()

	var saved []byte
	err = sqlitex.Execute(sl.conn, `SELECT spec FROM tasks WHERE id = ?`, &sqlitex.ExecOptions{
		Args: []any{task.ID},
		ResultFunc: func(stmt *sqlite.Stmt) error {
			saved = []byte(stmt.ColumnText(0))
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("load task: %w", err)
	}
	if saved != nil {
		if err := checkSame(saved, task); err != nil {
			return err
		}
	}

	at := now()
	err = sqlitex.Execute(sl.conn, `
		INSERT INTO tasks (id, spec, status, created_at, updated_at, current)
		VALUES (?, ?, 'running', ?, ?, 'starting')
		ON CONFLICT (id) DO UPDATE SET
			status = 'running', updated_at = excluded.updated_at,
			finished_at = NULL, result = NULL, current = 'resuming'`,
		&sqlitex.ExecOptions{Args: []any{task.ID, string(spec), at, at}})
	if err != nil {
		return fmt.Errorf("start task: %w", err)
	}
	return nil
}

// Append implements runner.Store. It saves the step and adds it to the
// task's summary in one transaction.
func (sl *SQLite) Append(ctx context.Context, taskID string, s runner.Step) (err error) {
	var msg []byte
	if s.Kind == runner.ToolSnapshot {
		msg, err = json.Marshal(s.Tools)
	} else {
		msg, err = json.Marshal(s.Msg)
	}
	if err != nil {
		return fmt.Errorf("encode msg: %w", err)
	}

	sl.mu.Lock()
	defer sl.mu.Unlock()
	defer sqlitex.Save(sl.conn)(&err)

	at := now()
	err = sqlitex.Execute(sl.conn,
		`INSERT INTO steps (task_id, kind, call_id, msg, at) VALUES (?, ?, ?, ?, ?)`,
		&sqlitex.ExecOptions{Args: []any{
			taskID, string(s.Kind), s.CallID, msg, at,
		}})
	if err != nil {
		return fmt.Errorf("append step: %w", err)
	}

	d := summarize(s)
	err = sqlitex.Execute(sl.conn, `
		UPDATE tasks SET
			steps = steps + 1,
			model_calls = model_calls + ?, tool_calls = tool_calls + ?,
			tool_errors = tool_errors + ?,
			tokens_in = tokens_in + ?, tokens_out = tokens_out + ?,
			current = COALESCE(NULLIF(?, ''), current),
			last_error = COALESCE(NULLIF(?, ''), last_error),
			updated_at = ?
		WHERE id = ?`,
		&sqlitex.ExecOptions{Args: []any{
			d.modelCalls, d.toolCalls, d.toolErrors, d.tokensIn, d.tokensOut,
			d.current, d.lastError, at, taskID,
		}})
	if err != nil {
		return fmt.Errorf("update summary: %w", err)
	}
	return nil
}

// delta is what one step adds to a task's summary.
type delta struct {
	modelCalls, toolCalls, toolErrors int
	tokensIn, tokensOut               int
	current                           string // "" = unchanged
	lastError                         string // "" = unchanged
}

func summarize(s runner.Step) delta {
	var d delta
	switch s.Kind {
	case runner.ModelReply:
		d.modelCalls = 1
		d.tokensIn, d.tokensOut = s.Msg.TokensIn, s.Msg.TokensOut
		if len(s.Msg.ToolCalls) > 0 {
			d.current = "running tools"
		}
	case runner.Intent:
		d.current = "calling a tool"
		if len(s.Msg.ToolCalls) > 0 {
			d.current = "calling " + s.Msg.ToolCalls[0].Name
		}
	case runner.ToolResult:
		d.toolCalls = 1
		if s.Msg.IsError {
			d.toolErrors = 1
			d.lastError = s.Msg.Text
		}
	}
	return d
}

// SetCurrent implements runner.Store.
func (sl *SQLite) SetCurrent(ctx context.Context, taskID, current string) error {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	err := sqlitex.Execute(sl.conn, `UPDATE tasks SET current = ?, updated_at = ? WHERE id = ?`,
		&sqlitex.ExecOptions{Args: []any{current, now(), taskID}})
	if err != nil {
		return fmt.Errorf("set current: %w", err)
	}
	return nil
}

// FinishTask implements runner.Store.
func (sl *SQLite) FinishTask(ctx context.Context, taskID, status, result, errText string) error {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	at := now()
	err := sqlitex.Execute(sl.conn, `
		UPDATE tasks SET
			status = ?, result = NULLIF(?, ''),
			last_error = COALESCE(NULLIF(?, ''), last_error),
			current = NULL, finished_at = ?, updated_at = ?
		WHERE id = ?`,
		&sqlitex.ExecOptions{Args: []any{status, result, errText, at, at, taskID}})
	if err != nil {
		return fmt.Errorf("finish task: %w", err)
	}
	return nil
}

// Summary is a task's row in the tasks table, without its spec.
type Summary struct {
	ID, Status, Model          string
	Created, Updated, Finished time.Time // Finished is zero while unfinished
	Steps, ModelCalls          int
	ToolCalls, ToolErrors      int
	TokensIn, TokensOut        int
	CostUSD                    float64
	Current, LastError, Result string
}

// Tasks returns every task's summary, the most recently updated first.
func (sl *SQLite) Tasks(ctx context.Context) ([]Summary, error) {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	var out []Summary
	err := sqlitex.Execute(sl.conn, `
		SELECT id, status, json_extract(spec, '$.Model'), created_at, updated_at,
			finished_at, steps, model_calls, tool_calls, tool_errors,
			tokens_in, tokens_out, cost_usd, current, last_error, result
		FROM tasks ORDER BY updated_at DESC`,
		&sqlitex.ExecOptions{ResultFunc: func(st *sqlite.Stmt) error {
			out = append(out, Summary{
				ID: st.ColumnText(0), Status: st.ColumnText(1), Model: st.ColumnText(2),
				Created: parseTime(st.ColumnText(3)), Updated: parseTime(st.ColumnText(4)),
				Finished: parseTime(st.ColumnText(5)),
				Steps:    st.ColumnInt(6), ModelCalls: st.ColumnInt(7),
				ToolCalls: st.ColumnInt(8), ToolErrors: st.ColumnInt(9),
				TokensIn: st.ColumnInt(10), TokensOut: st.ColumnInt(11),
				CostUSD: st.ColumnFloat(12),
				Current: st.ColumnText(13), LastError: st.ColumnText(14), Result: st.ColumnText(15),
			})
			return nil
		}})
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	return out, nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// parseTime parses a saved timestamp; "" (NULL) gives the zero time.
func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func (sl *SQLite) Load(ctx context.Context, taskID string) ([]runner.Step, error) {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	stmt, err := sl.conn.Prepare(`SELECT kind, call_id, msg FROM steps WHERE task_id = ? ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load steps: %w", err)
	}
	defer stmt.Reset()
	stmt.BindText(1, taskID)

	var steps []runner.Step
	for {
		hasRow, err := stmt.Step()
		if err != nil {
			return nil, fmt.Errorf("load steps: %w", err)
		}
		if !hasRow {
			return steps, nil
		}
		s := runner.Step{
			Kind:   runner.StepKind(stmt.ColumnText(0)),
			CallID: stmt.ColumnText(1),
		}
		var dst any = &s.Msg
		if s.Kind == runner.ToolSnapshot {
			dst = &s.Tools
		}
		if err := json.Unmarshal([]byte(stmt.ColumnText(2)), dst); err != nil {
			return nil, fmt.Errorf("decode msg: %w", err)
		}
		steps = append(steps, s)
	}
}

const schema = `
CREATE TABLE IF NOT EXISTS tasks (
	id          TEXT PRIMARY KEY,
	spec        BLOB NOT NULL,             -- the Task as JSON: goal, model, tools, limits
	status      TEXT NOT NULL              -- running | waiting | done | failed
	            CHECK (status IN ('running', 'waiting', 'done', 'failed')),
	created_at  TEXT NOT NULL,             -- RFC 3339, UTC, as in steps.at
	updated_at  TEXT NOT NULL,             -- last change to this row
	finished_at TEXT,                      -- set with done / failed

	-- summary, updated in the same transaction as each step append
	steps       INTEGER NOT NULL DEFAULT 0, -- rows in steps for this task
	model_calls INTEGER NOT NULL DEFAULT 0,
	tool_calls  INTEGER NOT NULL DEFAULT 0, -- results saved
	tool_errors INTEGER NOT NULL DEFAULT 0, -- results with IsError
	tokens_in   INTEGER NOT NULL DEFAULT 0,
	tokens_out  INTEGER NOT NULL DEFAULT 0,
	cost_usd    REAL    NOT NULL DEFAULT 0, -- 0 until cost accounting
	current     TEXT,                       -- e.g. "calling read_file", "asking anthropic:claude-sonnet-5"
	last_error  TEXT,                       -- why it failed, or the last failed tool call
	result      TEXT                        -- the final answer, when done
);
CREATE INDEX IF NOT EXISTS tasks_status ON tasks(status);

CREATE TABLE IF NOT EXISTS steps (
	id      INTEGER PRIMARY KEY, -- assigned by SQLite, always increasing
	task_id TEXT NOT NULL,
	kind    TEXT NOT NULL,
	call_id TEXT,
	msg     BLOB,                -- Message as JSON; []ToolDef for tool_snapshot
	at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS steps_task ON steps(task_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS one_result
	ON steps(task_id, call_id) WHERE kind = 'tool_result';
`
