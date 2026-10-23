package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/jrniemiec/keel/runner"
)

// checkSame returns runner.ErrTaskChanged, naming the fields that differ,
// when task doesn't match the spec saved on the task's first run.
func checkSame(saved []byte, task runner.Task) error {
	var old runner.Task
	if err := json.Unmarshal(saved, &old); err != nil {
		return fmt.Errorf("decode saved task: %w", err)
	}
	var diff []string
	ov, nv := reflect.ValueOf(old), reflect.ValueOf(task)
	for i := range ov.NumField() {
		o, n := ov.Field(i), nv.Field(i)
		if o.Kind() == reflect.Slice && o.Len() == 0 && n.Len() == 0 {
			continue // nil and empty are the same here
		}
		if !reflect.DeepEqual(o.Interface(), n.Interface()) {
			diff = append(diff, ov.Type().Field(i).Name)
		}
	}
	if len(diff) > 0 {
		return fmt.Errorf("%w: %s changed", runner.ErrTaskChanged, strings.Join(diff, ", "))
	}
	return nil
}
