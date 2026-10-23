// Package jsonc reads JSON with comments, for keel's config files. It
// blanks out // line comments, /* */ block comments and trailing commas,
// then decodes with encoding/json, so standard Go structs work unchanged.
// Copied from arc (arc/internal/jsonc); keel adds trailing commas, strict
// decoding and line numbers in errors.
package jsonc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Unmarshal decodes data into v. Unknown keys are an error, so a typo in a
// key isn't silently ignored; so is anything after the top-level value.
func Unmarshal(data []byte, v any) error {
	clean := strip(data)
	dec := json.NewDecoder(bytes.NewReader(clean))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return withLine(err, clean)
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("unexpected data after the top-level value")
	}
	return nil
}

// withLine adds the line number to errors that carry an offset. strip keeps
// every byte in place, so offsets match the original file.
func withLine(err error, data []byte) error {
	var offset int64
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax):
		offset = syntax.Offset
	case errors.As(err, &typ):
		offset = typ.Offset
	default:
		return err
	}
	line := 1 + bytes.Count(data[:min(int(offset), len(data))], []byte("\n"))
	return fmt.Errorf("line %d: %w", line, err)
}

// strip replaces comments and trailing commas with spaces, leaving string
// literals untouched. Newlines are kept, so the result has the same length
// and line breaks as src.
func strip(src []byte) []byte {
	out := bytes.Clone(src)
	lastComma := -1 // index of a comma with only blanks after it so far
	i := 0
	for i < len(out) {
		c := out[i]
		switch {
		case c == '"': // string: skip to the closing quote
			i++
			for i < len(out) && out[i] != '"' {
				if out[i] == '\\' {
					i++
				}
				i++
			}
			i++
			lastComma = -1
			continue
		case c == '/' && i+1 < len(out) && out[i+1] == '/': // line comment
			for i < len(out) && out[i] != '\n' {
				out[i] = ' '
				i++
			}
			continue
		case c == '/' && i+1 < len(out) && out[i+1] == '*': // block comment
			end := bytes.Index(out[i+2:], []byte("*/"))
			stop := len(out)
			if end >= 0 {
				stop = i + 2 + end + 2
			}
			for ; i < stop; i++ {
				if out[i] != '\n' {
					out[i] = ' '
				}
			}
			continue
		case c == ',':
			lastComma = i
		case c == '}' || c == ']':
			if lastComma >= 0 {
				out[lastComma] = ' ' // trailing comma
			}
			lastComma = -1
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			// blanks keep lastComma
		default:
			lastComma = -1
		}
		i++
	}
	return out
}
