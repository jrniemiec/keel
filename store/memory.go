package store

import (
	"context"
	"slices"

	"github.com/jrniemiec/keel/runner"
)

type Memory struct {
	store map[string][]runner.Step
}

func (ss *Memory) Append(ctx context.Context, taskID string, s runner.Step) error {

	ss.store[taskID] = append(ss.store[taskID], s)
	return nil
}
func (ss *Memory) Load(ctx context.Context, taskID string) ([]runner.Step, error) {
	return slices.Clone(ss.store[taskID]), nil
}

func NewMemory() runner.Store {

	return &Memory{store: map[string][]runner.Step{}}
}

// Memory keeps no task summaries; these do nothing.

func (ss *Memory) StartTask(ctx context.Context, task runner.Task) error { return nil }
func (ss *Memory) SetCurrent(ctx context.Context, taskID, current string) error {
	return nil
}
func (ss *Memory) FinishTask(ctx context.Context, taskID, status, result, errText string) error {
	return nil
}
