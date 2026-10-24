package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jrniemiec/keel/runner"
)

func TestServers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keel.jsonc")
	conf := `{
  "servers": {
    "fs": {"description": "Files", "command": "/bin/fs-server", "args": ["/data", "/more"]},
    "arc": {"command": "/bin/arc", "args": ["mcp"]},
  },
}`
	if err := os.WriteFile(path, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := servers([]string{"-c", path}, &out); err != nil {
		t.Fatal(err)
	}
	want := "NAME  COMMAND         ARGS         DESCRIPTION\narc   /bin/arc        mcp\nfs    /bin/fs-server  /data /more  Files\n"
	if out.String() != want {
		t.Errorf("output:\n%s\nwant:\n%s", out.String(), want)
	}

	var ue usageError
	if err := servers([]string{"-c", path, "fs"}, &bytes.Buffer{}); !errors.As(err, &ue) {
		t.Errorf("an argument: err = %v, want a usage error", err)
	}
}

func TestPrintTools(t *testing.T) {
	var out bytes.Buffer
	err := printTools(&out, []runner.ToolDef{
		{Name: "fs__read_file", Hints: runner.Hints{ReadOnly: true}},
		{Name: "fs__write_file", Hints: runner.Hints{Idempotent: true, Destructive: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"TOOL            READ-ONLY  IDEMPOTENT  DESTRUCTIVE\n",
		"fs__read_file   yes        no          no\n",
		"fs__write_file  no         yes         yes\n",
	} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("output lacks %q:\n%s", w, out.String())
		}
	}
}
