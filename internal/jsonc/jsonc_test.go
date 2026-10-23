package jsonc

import (
	"strings"
	"testing"
)

func TestUnmarshal(t *testing.T) {
	src := `{
	// a line comment
	"a": "x // not a comment", /* a block
	comment */
	"b": [1, 2, 3,],
	"c": "quote \" and /* still a string */",
}`
	var v struct {
		A string
		B []int
		C string
	}
	if err := Unmarshal([]byte(src), &v); err != nil {
		t.Fatal(err)
	}
	if v.A != "x // not a comment" || len(v.B) != 3 || v.C != `quote " and /* still a string */` {
		t.Errorf("got %+v", v)
	}
}

func TestUnmarshalErrors(t *testing.T) {
	type target struct{ A int }
	tests := []struct {
		name, src, want string
	}{
		{"unknown key", `{"a": 1, "b": 2}`, `unknown field "b"`},
		{"wrong type on line 3", "{\n// c\n\"a\": \"x\"}", "line 3"},
		{"syntax error", "{\n\"a\": 1\n\"b\": 2}", "line 3"},
		{"data after the value", `{"a": 1} {}`, "after the top-level value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v target
			err := Unmarshal([]byte(tt.src), &v)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestStripKeepsLength(t *testing.T) {
	src := "{\n /* a\n b */ \"a\": 1, // c\n}"
	out := strip([]byte(src))
	if len(out) != len(src) || strings.Count(string(out), "\n") != strings.Count(src, "\n") {
		t.Errorf("strip changed the layout: %q", out)
	}
}
