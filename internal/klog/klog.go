// Package klog is keel's logger: slog with a text handler, writing to one
// file with lumberjack rotation. It wraps slog, so the rest of keel imports
// only klog. Setup is trimmed from arc's clog (same in c2, lore).
//
// Call Init once from main. Then log with the package-level Info etc., or a
// Logger from Default().With(…), e.g. one per task.
package klog

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

const maxSizeMB = 50

// Level is a log level.
type Level = slog.Level

const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

// Logger writes log entries. Each entry is a fixed message (one of the Msg
// constants) and key-value pairs: Info(MsgToolCall, "tool", name).
type Logger struct{ l *slog.Logger }

func (g *Logger) Debug(msg string, args ...any) { g.l.Debug(msg, args...) }
func (g *Logger) Info(msg string, args ...any)  { g.l.Info(msg, args...) }
func (g *Logger) Warn(msg string, args ...any)  { g.l.Warn(msg, args...) }
func (g *Logger) Error(msg string, args ...any) { g.l.Error(msg, args...) }

// With returns a logger that adds args to every entry, e.g. With("task", id).
func (g *Logger) With(args ...any) *Logger { return &Logger{g.l.With(args...)} }

// Default returns the logger Init set up (stderr before Init).
func Default() *Logger { return &Logger{slog.Default()} }

// New returns a logger writing to w at level, independent of Init; for tests.
func New(w io.Writer, level Level) *Logger {
	return &Logger{slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))}
}

// Log with the default logger.
func Debug(msg string, args ...any) { slog.Debug(msg, args...) }
func Info(msg string, args ...any)  { slog.Info(msg, args...) }
func Warn(msg string, args ...any)  { slog.Warn(msg, args...) }
func Error(msg string, args ...any) { slog.Error(msg, args...) }

var (
	writer  *lumberjack.Logger
	level   slog.LevelVar // the file's level; SetLevel changes it while running
	verbose bool          // also write to stderr, at debug level
)

// ParseLevel converts a string (debug|info|warn|error) to a Level.
// Returns LevelInfo and false if the string is unrecognised.
func ParseLevel(s string) (Level, bool) {
	switch strings.ToLower(s) {
	case "debug":
		return LevelDebug, true
	case "info":
		return LevelInfo, true
	case "warn", "warning":
		return LevelWarn, true
	case "error":
		return LevelError, true
	default:
		return LevelInfo, false
	}
}

// Init opens path for append with automatic rotation and sets it as the slog
// default handler at lvl. When verb is true, a second handler also writes
// everything, debug included, to stderr; the file keeps lvl.
func Init(path string, lvl Level, verb bool) {
	if writer != nil {
		_ = writer.Close()
	}
	writer = &lumberjack.Logger{Filename: path, MaxSize: maxSizeMB, MaxBackups: 2}
	level.Set(lvl)
	verbose = verb

	var h slog.Handler = slog.NewTextHandler(writer, &slog.HandlerOptions{Level: &level})
	if verbose {
		stderr := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: LevelDebug})
		h = slog.NewMultiHandler(h, stderr)
	}
	slog.SetDefault(slog.New(h))
}

// SetLevel changes the file's level while running, e.g. to debug when a task
// looks wrong. All loggers from Default follow it.
func SetLevel(lvl Level) { level.Set(lvl) }

// IsDebug reports whether the file gets debug entries; check it before
// building an expensive payload.
func IsDebug() bool { return level.Level() <= LevelDebug }

// Raw writes a multi-line block (a prompt, a tool's output) without slog's
// quoting, so it stays readable. label is its header. Written only at debug
// level; also to stderr when verbose.
func Raw(label, text string) {
	if writer == nil || (!IsDebug() && !verbose) {
		return
	}
	ts := time.Now().Format("2006-01-02T15:04:05.000Z07:00")
	block := fmt.Sprintf("\n%s [RAW] %s\n%s\n", ts, label, text)
	if IsDebug() {
		_, _ = io.WriteString(writer, block)
	}
	if verbose {
		fmt.Fprint(os.Stderr, block)
	}
}
