// Package ledger implements the append-only JSONL event log that is the single
// source of truth for the orchestrator (docs/v2/adr/001-event-sourced-truth.md).
//
// The log is newline-delimited JSON: one [api.Event] per line, in append order.
// Sequence numbers are assigned on append and are monotonic starting at 1.
package ledger

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// Ledger is a concurrency-safe append-only event log backed by a JSONL file.
type Ledger struct {
	mu      sync.Mutex
	path    string
	nextSeq int
}

// Open opens (or creates) the ledger at path, creating parent directories as
// needed. It scans any existing events to resume sequence numbering.
func Open(path string) (*Ledger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create ledger dir: %w", err)
	}
	l := &Ledger{path: path, nextSeq: 1}
	events, err := l.Read()
	if err != nil {
		return nil, err
	}
	if n := len(events); n > 0 {
		l.nextSeq = events[n-1].Seq + 1
	}
	return l, nil
}

// Append assigns the next sequence number to e, writes it as one JSONL line,
// and returns the stored event. Safe for concurrent use.
func (l *Ledger) Append(e api.Event) (api.Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	e.Seq = l.nextSeq
	line, err := json.Marshal(e)
	if err != nil {
		return api.Event{}, fmt.Errorf("marshal event: %w", err)
	}

	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return api.Event{}, fmt.Errorf("open ledger: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return api.Event{}, fmt.Errorf("write event: %w", err)
	}

	l.nextSeq++
	return e, nil
}

// Read returns all events in append order. A missing file yields no events.
func (l *Ledger) Read() ([]api.Event, error) {
	var events []api.Event
	if err := l.Replay(func(e api.Event) error {
		events = append(events, e)
		return nil
	}); err != nil {
		return nil, err
	}
	return events, nil
}

// Replay streams each event through fn in append order. If fn returns an error,
// replay stops and returns it. A missing file is treated as an empty log.
func (l *Ledger) Replay(fn func(api.Event) error) error {
	f, err := os.Open(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open ledger: %w", err)
	}
	defer f.Close()

	r := bufio.NewReader(f)
	dec := json.NewDecoder(r)
	for {
		var e api.Event
		if err := dec.Decode(&e); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("decode event: %w", err)
		}
		if err := fn(e); err != nil {
			return err
		}
	}
}
