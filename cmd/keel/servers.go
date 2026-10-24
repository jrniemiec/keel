package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/jrniemiec/keel/config"
	"github.com/jrniemiec/keel/internal/klog"
	"github.com/jrniemiec/keel/mcphost"
	"github.com/jrniemiec/keel/runner"
)

// serversFlags holds keel servers' flags.
type serversFlags struct {
	config      string
	tools, help bool
}

// newServersFlags defines keel servers' flags. They are described in helpText.
func newServersFlags() (*flag.FlagSet, *serversFlags) {
	fs := flag.NewFlagSet("servers", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are reported by main, help by helpText
	f := &serversFlags{}
	for _, n := range []string{"c", "config"} {
		fs.StringVar(&f.config, n, "", "")
	}
	fs.BoolVar(&f.tools, "tools", false, "")
	for _, n := range []string{"h", "help"} {
		fs.BoolVar(&f.help, n, false, "")
	}
	return fs, f
}

// servers is "keel servers": the servers in the keel config, or with --tools
// the tools they offer.
func servers(args []string, w io.Writer) error {
	flags, f := newServersFlags()
	rest, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	if f.help {
		fmt.Fprint(w, helpText)
		return nil
	}
	if len(rest) > 0 {
		return usageError{fmt.Sprintf("keel servers takes no arguments, got %q", rest[0])}
	}

	kc, err := config.LoadKeelConfig(f.config)
	if err != nil {
		return err
	}
	names := slices.Sorted(maps.Keys(kc.Servers))
	if len(names) == 0 {
		fmt.Fprintln(w, "no servers")
		return nil
	}
	if !f.tools {
		return printServers(w, kc, names)
	}

	// Server output (stderr, hint overrides matching no tool) goes to the log file.
	level, _ := klog.ParseLevel(kc.LogLevel)
	klog.Init(kc.LogFile, level, false, kc.LogMaxMB, kc.LogBackups)
	ctx := context.Background()
	host, err := mcphost.Start(ctx, kc.MCPServers(names), kc.MaxToolOutput)
	if err != nil {
		return err
	}
	defer host.Close()
	tools, err := host.List(ctx)
	if err != nil {
		return err
	}
	return printTools(w, tools)
}

// printServers writes one row per server, in name order, with the
// description from the keel config.
func printServers(w io.Writer, kc config.KeelConfig, names []string) error {
	var rows []row
	for _, n := range names {
		s := kc.Servers[n]
		rows = append(rows, row{cells: fmt.Sprintf("%s\t%s\t%s\t%s", n, s.Command, strings.Join(s.Args, " "), s.Description)})
	}
	return printTable(w, "NAME\tCOMMAND\tARGS\tDESCRIPTION", rows)
}

// printTools writes one row per tool: its full name, as used in a task's
// allow list, and its hints after the keel config's overrides.
func printTools(w io.Writer, tools []runner.ToolDef) error {
	if len(tools) == 0 {
		fmt.Fprintln(w, "no tools")
		return nil
	}
	yesNo := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	var rows []row
	for _, t := range tools {
		h := t.Hints
		rows = append(rows, row{cells: fmt.Sprintf("%s\t%s\t%s\t%s",
			t.Name, yesNo(h.ReadOnly), yesNo(h.Idempotent), yesNo(h.Destructive))})
	}
	return printTable(w, "TOOL\tREAD-ONLY\tIDEMPOTENT\tDESTRUCTIVE", rows)
}
