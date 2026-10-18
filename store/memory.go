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
