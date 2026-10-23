package main

import (
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
	for _, fs := range []*flag.FlagSet{run, tasks, steps} {
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
