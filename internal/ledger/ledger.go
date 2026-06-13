// Package ledger implements the append-only JSONL event log that is the single
// source of truth for the orchestrator (docs/v2/adr/001-event-sourced-truth.md).
//
// The log is newline-delimited JSON: one [api.Event] per line, in append order.
// Sequence numbers are assigned on append and are monotonic starting at 1.
package ledger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	events, validLen, err := scan(path)
	if err != nil {
		return nil, err
	}
	// Repair a torn tail (e.g. a crash mid-Append) so future appends are clean:
	// truncate anything past the last complete, parseable line. The discarded
	// event is re-emitted on the next Append; idempotency keys make that safe.
	if fi, statErr := os.Stat(path); statErr == nil && fi.Size() > validLen {
		if err := os.Truncate(path, validLen); err != nil {
			return nil, fmt.Errorf("truncate torn ledger tail: %w", err)
		}
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

// Read returns all events in append order. A missing file yields no events; a
// torn/partial trailing line (from a crash mid-Append) is tolerated and skipped.
func (l *Ledger) Read() ([]api.Event, error) {
	events, _, err := scan(l.path)
	return events, err
}

// Replay streams each event through fn in append order. If fn returns an error,
// replay stops and returns it. A missing file is treated as an empty log.
func (l *Ledger) Replay(fn func(api.Event) error) error {
	events, _, err := scan(l.path)
	if err != nil {
		return err
	}
	for _, e := range events {
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

// scan reads the JSONL log at path and returns the decoded events plus validLen:
// the byte length of the valid prefix (just past the last complete, parseable
// line). A torn or partially-written trailing line is tolerated — excluded from
// both the events and validLen so the caller can truncate it away. A corrupt
// line that is followed by further lines is treated as real corruption and
// returns an error. A missing file yields no events.
func scan(path string) (events []api.Event, validLen int64, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("open ledger: %w", err)
	}
	for i := 0; i < len(data); {
		j := bytes.IndexByte(data[i:], '\n')
		if j < 0 {
			// Trailing bytes with no newline: a torn tail. Stop; do not count it.
			break
		}
		lineEnd := i + j
		line := strings.TrimSpace(string(data[i:lineEnd]))
		if line != "" {
			var e api.Event
			if jerr := json.Unmarshal([]byte(line), &e); jerr != nil {
				if lineEnd+1 < len(data) {
					return nil, 0, fmt.Errorf("corrupt event before byte %d: %w", lineEnd, jerr)
				}
				// Corrupt final line: treat as a torn tail and stop.
				break
			}
			events = append(events, e)
		}
		validLen = int64(lineEnd + 1)
		i = lineEnd + 1
	}
	return events, validLen, nil
}
