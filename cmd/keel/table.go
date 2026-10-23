package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/jrniemiec/keel/config"
	"github.com/jrniemiec/keel/store"
)

// parseArgs parses flags, which may come before or after the arguments, and
// returns the arguments.
func parseArgs(flags *flag.FlagSet, args []string) ([]string, error) {
	var rest []string
	for {
		if err := flags.Parse(args); err != nil {
			return nil, usageError{err.Error()}
		}
		if flags.NArg() == 0 {
			return rest, nil
		}
		rest = append(rest, flags.Arg(0))
		args = flags.Args()[1:]
	}
}

// openDB opens the database at dbPath, or the one in the keel config at
// configPath when dbPath is "". Unlike keel run, it doesn't create one.
func openDB(configPath, dbPath string) (*store.SQLite, error) {
	if dbPath == "" {
		kc, err := config.LoadKeelConfig(configPath)
		if err != nil {
			return nil, err
		}
		dbPath = kc.DB
	}
	if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no database at %s", dbPath)
	}
	return store.OpenSQLite(dbPath)
}

// row is a table row: tab-separated cells, and text printed in full on the
// lines below it (--full).
type row struct {
	cells string
	below string
}

// printTable aligns the rows' cells under header and prints each row's
// below text, indented, after it.
func printTable(w io.Writer, header string, rows []row) error {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, header)
	for _, r := range rows {
		fmt.Fprintln(tw, r.cells)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	fmt.Fprintln(w, strings.TrimRight(lines[0], " "))
	for i, r := range rows {
		fmt.Fprintln(w, strings.TrimRight(lines[i+1], " "))
		if r.below != "" {
			for _, l := range strings.Split(strings.TrimRight(r.below, "\n"), "\n") {
				fmt.Fprintln(w, "    "+l)
			}
		}
	}
	return nil
}

// squeeze puts s on one line: runs of white space, newlines included,
// become one space.
func squeeze(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// oneLine squeezes s onto one line of at most max characters.
func oneLine(s string, max int) string {
	s = squeeze(s)
	if r := []rune(s); len(r) > max {
		s = string(r[:max-3]) + "..."
	}
	return s
}
