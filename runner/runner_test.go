package runner

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestMatchTools(t *testing.T) {
	listed := []ToolDef{{Name: "fs__read_file"}, {Name: "fs__read_dir"}, {Name: "fs__write_file"}, {Name: "web__fetch"}}
	tests := []struct {
		name          string
		patterns      []string
		wantMatched   []string
		wantUnmatched []string
	}{
		{"exact", []string{"fs__read_file"}, []string{"fs__read_file"}, nil},
		{"star", []string{"fs__read_*"}, []string{"fs__read_file", "fs__read_dir"}, nil},
		{"question mark", []string{"fs__read_di?"}, []string{"fs__read_dir"}, nil},
		{"class", []string{"fs__[rw]*_file"}, []string{"fs__read_file", "fs__write_file"}, nil},
		{"overlapping patterns", []string{"fs__*", "fs__read_file"}, []string{"fs__read_file", "fs__read_dir", "fs__write_file"}, nil},
		{"no match", []string{"fs__raed_file", "web__fetch"}, []string{"web__fetch"}, []string{"fs__raed_file"}},
		{"bad pattern", []string{"fs__[read"}, nil, []string{"fs__[read"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, unmatchedPatterns := matchTools(listed, tt.patterns)
			var names []string
			for _, tool := range matched {
				names = append(names, tool.Name)
			}
			if !slices.Equal(names, tt.wantMatched) {
				t.Errorf("matched = %v, want %v", names, tt.wantMatched)
			}
			if !slices.Equal(unmatchedPatterns, tt.wantUnmatched) {
				t.Errorf("unmatchedPatterns = %v, want %v", unmatchedPatterns, tt.wantUnmatched)
			}
		})
	}
}

// listTools is a Tools that only lists.
type listTools []ToolDef

func (l listTools) List(ctx context.Context) ([]ToolDef, error) { return l, nil }
func (l listTools) Call(ctx context.Context, call ToolCall) (string, bool, error) {
	panic("not called")
}

// startStore records StartTask; its other methods are not called.
type startStore struct {
	Store
	started bool
}

func (s *startStore) StartTask(ctx context.Context, task Task) error {
	s.started = true
	return nil
}

func TestRunUnmatchedPatternSavesNothing(t *testing.T) {
	store := &startStore{}
	r := &Runner{Model: modelFunc(func() { t.Fatal("model called") }), Tools: listTools{{Name: "fs__read_file"}}, Store: store}
	task := Task{ID: "t1", Goal: "g", Model: "m", MaxSteps: 1, AllowedTools: []string{"fs__read_file", "fs__raed_file"}}

	_, err := r.Run(context.Background(), task)
	if err == nil || !strings.Contains(err.Error(), `allow ["fs__raed_file"]: no tool matches`) {
		t.Errorf("err = %v, want no tool matches", err)
	}
	if store.started {
		t.Error("task saved before the allow-list was checked")
	}
}

// modelFunc is a Model that calls f and returns an empty reply.
type modelFunc func()

func (f modelFunc) Ask(ctx context.Context, model string, msgs []Message, tools []ToolDef) (Message, error) {
	f()
	return Message{}, nil
}
