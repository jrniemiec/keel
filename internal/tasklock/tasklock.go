// Package tasklock keeps two keel processes from running the same task: both
// would replay the same log and could run the same tool call. The lock is a
// flock on a file per task; the kernel frees it when the process ends in any
// way (exit, panic, kill -9), so a lock is never left behind. It needs a
// local disk: on NFS or a synced folder flock may do nothing. See
// local/design-notes.md, "Lock per task".
package tasklock

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ErrLocked is returned by Lock when another process holds the task's lock.
var ErrLocked = errors.New("task is running in another keel process")

// Lock takes the lock for taskID in dir, without waiting; dir and the file
// are created if missing. It writes this process's PID into the file, for the
// message another process shows. unlock releases it; so does the process's
// exit, crash or kill. The file is never deleted: a process could then lock
// a file that's gone while another locks a new one.
func Lock(dir, taskID string) (unlock func(), err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("lock task %s: %w", taskID, err)
	}
	// Go opens files close-on-exec, so MCP servers (child processes) don't
	// inherit the lock.
	f, err := os.OpenFile(filepath.Join(dir, url.PathEscape(taskID)+".lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("lock task %s: %w", taskID, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		pid := holder(f)
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: task %s (pid %s)", ErrLocked, taskID, pid)
		}
		return nil, fmt.Errorf("lock task %s: %w", taskID, err)
	}
	// The PID only after locking, so a process that didn't get the lock
	// can't overwrite it. A damaged file only spoils the message.
	if err := f.Truncate(0); err == nil {
		f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0)
	}
	return func() { f.Close() }, nil
}

// holder returns the PID in the lock file, or "unknown".
func holder(f *os.File) string {
	b := make([]byte, 20)
	n, _ := f.ReadAt(b, 0)
	if pid := strings.TrimSpace(string(b[:n])); pid != "" {
		return pid
	}
	return "unknown"
}
