package main

import (
	"errors"
	"flag"
	"strings"
	"testing"
)

// Every flag except --crash-at is in helpText, so the help can't drift from
// the flags.
func TestHelpListsFlags(t *testing.T) {
	run, _ := newRunFlags()
	tasks, _ := newShowFlags("tasks", true)
	steps, _ := newShowFlags("steps", false)
	servers, _ := newServersFlags()
	for _, fs := range []*flag.FlagSet{run, tasks, steps, servers} {
		fs.VisitAll(func(f *flag.Flag) {
			if f.Name == "crash-at" {
				return
			}
			want := "--" + f.Name
			if len(f.Name) == 1 {
				want = "-" + f.Name + ", --"
			}
			if !strings.Contains(helpText, want) {
				t.Errorf("helpText lacks %q (keel %s)", want, fs.Name())
			}
		})
	}
}

func TestRunQuietAndVerbose(t *testing.T) {
	err := run([]string{"-q", "-v", "notes"})
	var usage usageError
	if !errors.As(err, &usage) || !strings.Contains(err.Error(), "--quiet and --verbose") {
		t.Errorf("err = %v, want a usage error naming both flags", err)
	}
}
