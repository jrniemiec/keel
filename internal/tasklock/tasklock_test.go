package tasklock

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary hold a lock as a child process: with
// TASKLOCK_HOLD=<dir> it locks task t1 there, prints "locked" and sleeps.
func TestMain(m *testing.M) {
	if dir := os.Getenv("TASKLOCK_HOLD"); dir != "" {
		if _, err := Lock(dir, "t1"); err != nil {
			os.Exit(1)
		}
		os.Stdout.WriteString("locked\n")
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestLock(t *testing.T) {
	dir := t.TempDir() + "/locks" // created by Lock
	unlock, err := Lock(dir, "t1")
	if err != nil {
		t.Fatal(err)
	}

	// flock locks belong to an open file, so a second open in this process
	// conflicts too.
	_, err = Lock(dir, "t1")
	if !errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), "pid "+strconv.Itoa(os.Getpid())) {
		t.Errorf("second Lock: err = %v, want ErrLocked with this PID", err)
	}
	unlockOther, err := Lock(dir, "t2")
	if err != nil {
		t.Errorf("another task: %v", err)
	} else {
		unlockOther()
	}

	unlock()
	unlock, err = Lock(dir, "t1")
	if err != nil {
		t.Fatalf("after unlock: %v", err)
	}
	unlock()
}

func TestLockEscapesTaskID(t *testing.T) {
	dir := t.TempDir()
	unlock, err := Lock(dir, "a/b")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := os.Stat(dir + "/a%2Fb.lock"); err != nil {
		t.Error(err)
	}
}

// A process that holds the lock and is killed frees it.
func TestLockFreedWhenKilled(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "TASKLOCK_HOLD="+dir)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if line, _ := bufio.NewReader(out).ReadString('\n'); line != "locked\n" {
		cmd.Process.Kill()
		t.Fatalf("child said %q", line)
	}
	_, err = Lock(dir, "t1")
	if !errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), "pid "+strconv.Itoa(cmd.Process.Pid)) {
		t.Errorf("err = %v, want ErrLocked with the child's PID", err)
	}

	cmd.Process.Kill()
	cmd.Wait()
	unlock, err := Lock(dir, "t1")
	if err != nil {
		t.Fatalf("after kill: %v", err)
	}
	unlock()
}

// A child process (as an MCP server is) doesn't inherit the lock: once the
// parent unlocks, the task is free while the child still runs.
func TestLockNotInherited(t *testing.T) {
	dir := t.TempDir()
	unlock, err := Lock(dir, "t1")
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		child.Process.Kill()
		child.Wait()
	}()

	unlock()
	unlock, err = Lock(dir, "t1")
	if err != nil {
		t.Fatalf("with the child running: %v", err)
	}
	unlock()
}
