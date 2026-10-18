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

func (sl *SQLite) Append(ctx context.Context, taskID string, s runner.Step) error {
	msg, err := json.Marshal(s.Msg)
	if err != nil {
		return fmt.Errorf("encode msg: %w", err)

	}

	sl.mu.Lock()
	defer sl.mu.Unlock()

	err = sqlitex.Execute(sl.conn,
		`INSERT INTO steps (task_id, kind, call_id, msg, at) VALUES (?, ?, ?, ?, ?)`,
		&sqlitex.ExecOptions{Args: []any{
			taskID, string(s.Kind), s.CallID, msg, time.Now().UTC().Format(time.RFC3339Nano),
		}})
	if err != nil {
		return fmt.Errorf("append step: %w", err)
	}

	return nil
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
		if err := json.Unmarshal([]byte(stmt.ColumnText(2)), &s.Msg); err != nil {
			return nil, fmt.Errorf("decode msg: %w", err)
		}
		steps = append(steps, s)
	}
}

const schema = `
CREATE TABLE IF NOT EXISTS steps (
	id      INTEGER PRIMARY KEY, -- assigned by SQLite, always increasing
	task_id TEXT NOT NULL,
	kind    TEXT NOT NULL,
	call_id TEXT,
	msg     BLOB,
	at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS steps_task ON steps(task_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS one_result
	ON steps(task_id, call_id) WHERE kind = 'tool_result';
`
