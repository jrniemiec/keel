package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/jrniemiec/keel/runner"
)

// switchable are the Task fields a resume may change: a task can continue
// with another model, e.g. when its provider is down.
var switchable = map[string]bool{"Model": true, "Thinking": true}

// checkSame returns runner.ErrTaskChanged, naming the fields that differ,
// when task doesn't match the saved spec; switchable fields are not compared.
// A task saved before approvals has no Approval and takes the new one.
func checkSame(saved []byte, task runner.Task) error {
	var old runner.Task
	if err := json.Unmarshal(saved, &old); err != nil {
		return fmt.Errorf("decode saved task: %w", err)
	}
	if old.Approval == (runner.Approval{}) {
		old.Approval = task.Approval // saved before approvals existed
	}
	var diff []string
	ov, nv := reflect.ValueOf(old), reflect.ValueOf(task)
	for i := range ov.NumField() {
		if switchable[ov.Type().Field(i).Name] {
			continue
		}
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
