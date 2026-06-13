package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// File is a file the mock backend writes into the worktree.
type File struct {
	Path    string // relative to the worktree
	Content string
}

// Mock is a deterministic, offline Backend used for tests and demos. It makes
// no network calls: it simply writes a configured set of files into the
// worktree, so the entire orchestration loop can run inside `go test`.
type Mock struct {
	// Plan maps a ticket Title to the files to write. The key "*" is a default
	// applied to any title without a specific entry. If neither is present, the
	// mock writes a single marker file named "<TicketID>.txt".
	Plan map[string][]File
	// FailTitles, when true for a title, makes Run return an error (used to
	// exercise the verification-failure / retry path).
	FailTitles map[string]bool
}

// NewMock returns an empty Mock with default marker-file behavior.
func NewMock() *Mock { return &Mock{} }

// Name implements Backend.
func (*Mock) Name() string { return "mock" }

// Run implements Backend: it writes the planned files into task.Worktree.
func (m *Mock) Run(ctx context.Context, task Task) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if m.FailTitles[task.Title] {
		return Result{}, fmt.Errorf("mock: forced failure for %q", task.Title)
	}

	files, ok := m.Plan[task.Title]
	if !ok {
		files = m.Plan["*"]
	}
	if files == nil {
		files = []File{{Path: task.TicketID + ".txt", Content: task.Title + "\n"}}
	}

	for _, f := range files {
		dst := filepath.Join(task.Worktree, f.Path)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return Result{}, fmt.Errorf("mock: mkdir for %s: %w", f.Path, err)
		}
		if err := os.WriteFile(dst, []byte(f.Content), 0o644); err != nil {
			return Result{}, fmt.Errorf("mock: write %s: %w", f.Path, err)
		}
	}

	return Result{
		Trace:   fmt.Sprintf("mock wrote %d file(s) for %q", len(files), task.Title),
		Summary: task.Title,
	}, nil
}
