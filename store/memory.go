package store

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/jrniemiec/keel/runner"
)

type Memory struct {
	store map[string][]runner.Step
	specs map[string][]byte // each task as first started, as JSON
}

func (ss *Memory) Append(ctx context.Context, taskID string, s runner.Step) error {

	ss.store[taskID] = append(ss.store[taskID], s)
	return nil
}
func (ss *Memory) Load(ctx context.Context, taskID string) ([]runner.Step, error) {
	return slices.Clone(ss.store[taskID]), nil
}

func NewMemory() runner.Store {

	return &Memory{store: map[string][]runner.Step{}, specs: map[string][]byte{}}
}

// StartTask implements runner.Store: a resumed task must match its first run,
// except for the model and thinking, which are saved.
func (ss *Memory) StartTask(ctx context.Context, task runner.Task) error {
	if saved, ok := ss.specs[task.ID]; ok {
		if err := checkSame(saved, task); err != nil {
			return err
		}
	}
	spec, err := json.Marshal(task)
	if err != nil {
		return err
	}
	ss.specs[task.ID] = spec
	return nil
}

// Memory keeps no task summaries; these do nothing.

func (ss *Memory) SetCurrent(ctx context.Context, taskID, current string) error {
	return nil
}
func (ss *Memory) FinishTask(ctx context.Context, taskID, status, result, errText string) error {
	return nil
}
func (ss *Memory) BlockTask(ctx context.Context, taskID, reason string) error {
	return nil
}
