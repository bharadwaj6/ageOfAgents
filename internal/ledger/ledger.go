// Package ledger implements the append-only JSONL event log that is the single
// source of truth for the orchestrator (docs/design/adr/001-event-sourced-truth.md).
//
// The log is newline-delimited JSON: one [api.Event] per line, in append order.
// Sequence numbers are assigned on append and are monotonic starting at 1.
package ledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bharadwaj6/ageOfAgents/internal/filelock"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// lockSuffix names the sidecar file writers lock: path+lockSuffix. It is locked
// rather than the log itself because Windows locks are mandatory — locking the
// log would make lock-free readers fail. The file is never deleted.
const lockSuffix = ".lock"

// Ledger is an append-only event log backed by a JSONL file. It is safe for
// concurrent use by many goroutines, and by many Ledger values — in one process
// or in several — open on the same path: every Append, Update and Open holds
// an exclusive lock on a sidecar file, path+".lock", so sequence numbers stay
// gapless and a writer's half-written line is never mistaken for a crash.
//
// The lock is advisory (flock on Unix, LockFileEx on Windows) and assumes a
// local filesystem: it is unreliable on NFS and on some container bind mounts.
// Read and Replay take no lock.
type Ledger struct {
	mu       sync.Mutex // serialises this value's writes; the file lock serialises the rest
	path     string
	lockPath string
	nextSeq  int
	size     int64           // bytes of log this value has accounted for: a line boundary
	onAppend func(api.Event) // optional; called under mu in append order
}

// SetAppendHook registers fn to be called with each event right after it is
// durably appended, in sequence order (it runs under the ledger lock, so it must
// be fast and non-blocking — e.g. enqueue, don't do I/O). Used to stream events
// to a live observer such as the OTel exporter; pass nil to clear. The hook is
// purely observational and never affects the log. It sees only the events this
// Ledger appends, not those appended through other handles.
func (l *Ledger) SetAppendHook(fn func(api.Event)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.onAppend = fn
}

// Open opens (or creates) the ledger at path, creating parent directories as
// needed. It scans any existing events to resume sequence numbering. If another
// writer is appending, Open waits for it to finish.
//
// A log whose lock file cannot be created (a read-only directory, say) still
// opens for reading. Nothing can be appended through it, since Append needs the
// lock, so it is scanned without the torn-tail repair that only a lock holder
// may do; the first successful Append repairs it instead.
func Open(path string) (*Ledger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create ledger dir: %w", err)
	}
	l := &Ledger{path: path, lockPath: path + lockSuffix, nextSeq: 1}
	err := l.withLock(l.sync)
	var unavailable *lockUnavailableError
	if errors.As(err, &unavailable) {
		events, validLen, serr := scan(path)
		if serr != nil {
			return nil, serr
		}
		l.size = validLen
		if n := len(events); n > 0 {
			l.nextSeq = events[n-1].Seq + 1
		}
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	return l, nil
}

// lockUnavailableError reports that the sidecar lock file could not be opened,
// so no lock can be taken through this path.
type lockUnavailableError struct{ err error }

func (e *lockUnavailableError) Error() string { return "open ledger lock: " + e.err.Error() }
func (e *lockUnavailableError) Unwrap() error { return e.err }

// Append assigns the next sequence number to e, writes it as one JSONL line,
// and returns the stored event. Safe for concurrent use, including by other
// Ledgers and other processes appending to the same path.
func (l *Ledger) Append(e api.Event) (api.Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var stored []api.Event
	err := l.withLock(func() error {
		// Another writer may have appended since this Ledger last looked.
		if err := l.sync(); err != nil {
			return err
		}
		var err error
		stored, err = l.write([]api.Event{e})
		return err
	})
	if err != nil {
		return api.Event{}, err
	}
	l.notify(stored)
	return stored[0], nil
}

// Update is a read-decide-append transaction: it reads the whole log, passes
// it to decide, and appends whatever events decide returns — possibly none —
// assigning sequence numbers exactly as [Ledger.Append] does. It returns the
// stored events, in order.
//
// No other writer, in this process or another, can append between the read and
// the write, so a check made in decide ("is this key already on the log?",
// "is this ticket still awaiting approval?") still holds when its events land.
// That is what makes a check-then-append idempotent under concurrent callers.
//
// If decide returns an error, nothing is appended and Update returns that
// error. decide must not call back into this Ledger (Append and Update would
// deadlock), and it blocks every writer on the log while it runs, so it should
// be a fold and a decision — milliseconds — never I/O or a model call.
func (l *Ledger) Update(decide func(events []api.Event) ([]api.Event, error)) ([]api.Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var stored []api.Event
	err := l.withLock(func() error {
		if err := l.sync(); err != nil {
			return err
		}
		// sync has repaired any torn tail, so this is exactly the log the new
		// events will follow.
		events, _, err := scan(l.path)
		if err != nil {
			return err
		}
		pending, err := decide(events)
		if err != nil {
			return err
		}
		stored, err = l.write(pending)
		return err
	})
	if err != nil {
		return nil, err
	}
	l.notify(stored)
	return stored, nil
}

// write appends events to the log as consecutive sequence numbers from
// nextSeq, in a single write, and returns them as stored. The caller must hold
// mu and the file lock, and have just synced.
func (l *Ledger) write(events []api.Event) ([]api.Event, error) {
	if len(events) == 0 {
		return nil, nil
	}
	stored := make([]api.Event, len(events))
	var buf []byte
	for i, e := range events {
		e.Seq = l.nextSeq + i
		line, err := json.Marshal(e)
		if err != nil {
			return nil, fmt.Errorf("marshal event: %w", err)
		}
		buf = append(append(buf, line...), '\n')
		stored[i] = e
	}
	if err := appendLine(l.path, buf); err != nil {
		// A failed write may have left part of a line; forget what we know so
		// the next sync rescans the log and repairs it.
		l.size, l.nextSeq = 0, 1
		return nil, err
	}
	l.size += int64(len(buf))
	l.nextSeq += len(events)
	return stored, nil
}

// notify delivers stored events to the append hook, if any. The caller holds
// mu, so events are delivered in sequence order.
func (l *Ledger) notify(stored []api.Event) {
	if l.onAppend == nil {
		return
	}
	for _, e := range stored {
		l.onAppend(e)
	}
}

// appendLine writes line to the end of the log at path, creating it if needed.
func appendLine(path string, line []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open ledger: %w", err)
	}
	if _, err := f.Write(line); err != nil {
		return errors.Join(fmt.Errorf("write event: %w", err), f.Close())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close ledger: %w", err)
	}
	return nil
}

// withLock runs fn holding the exclusive cross-process lock on the sidecar file.
func (l *Ledger) withLock(fn func() error) error {
	f, err := os.OpenFile(l.lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return &lockUnavailableError{err: err}
	}
	if err := filelock.Lock(f); err != nil {
		return errors.Join(fmt.Errorf("lock ledger: %w", err), f.Close())
	}
	fnErr := fn()
	var unlockErr, closeErr error
	if err := filelock.Unlock(f); err != nil {
		unlockErr = fmt.Errorf("unlock ledger: %w", err)
	}
	if err := f.Close(); err != nil {
		closeErr = fmt.Errorf("close ledger lock: %w", err)
	}
	return errors.Join(fnErr, unlockErr, closeErr)
}

// sync brings nextSeq and size up to date with the log on disk, repairing a
// torn tail. The caller must hold the file lock, so no writer is mid-line: a
// partial last line can only be a crashed writer's, and is truncated away (the
// event it held is re-emitted on retry; idempotency keys make that safe).
//
// The log only grows, so when it has grown only the new bytes are parsed. If
// it shrank, or the new bytes do not parse, it was changed outside the ledger
// and is rescanned from the start.
func (l *Ledger) sync() error {
	size, err := fileSize(l.path)
	if err != nil {
		return err
	}
	if size == l.size {
		return nil
	}
	if size < l.size {
		l.size, l.nextSeq = 0, 1
	}
	events, validLen, err := scanFrom(l.path, l.size)
	if err != nil && l.size > 0 {
		l.size, l.nextSeq = 0, 1
		events, validLen, err = scanFrom(l.path, 0)
	}
	if err != nil {
		return err
	}
	if size > validLen {
		if err := os.Truncate(l.path, validLen); err != nil {
			return fmt.Errorf("truncate torn ledger tail: %w", err)
		}
	}
	l.size = validLen
	if n := len(events); n > 0 {
		l.nextSeq = events[n-1].Seq + 1
	}
	return nil
}

// fileSize returns the size of the file at path; a missing file is empty.
func fileSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("stat ledger: %w", err)
	}
	return fi.Size(), nil
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

// scan reads the whole JSONL log at path; see scanFrom.
func scan(path string) (events []api.Event, validLen int64, err error) {
	return scanFrom(path, 0)
}

// scanFrom reads the JSONL log at path from byte offset and returns the decoded
// events plus validLen: the absolute offset just past the last complete,
// parseable line (offset itself if there is none). A torn or
// partially-written trailing line is tolerated — excluded from both the events
// and validLen so the caller can truncate it away. A corrupt line that is
// followed by further lines is treated as real corruption and returns an
// error, as does an offset that is not the start of a line (so a stale offset
// can never cut a real line in half). A missing file yields no events.
func scanFrom(path string, offset int64) (events []api.Event, validLen int64, err error) {
	// A line starts at offset only if the byte before it ends the previous
	// line, so read that byte too.
	data, err := readFrom(path, max(offset-1, 0))
	if err != nil {
		return nil, 0, err
	}
	if offset > 0 {
		if len(data) == 0 || data[0] != '\n' {
			return nil, 0, fmt.Errorf("ledger offset %d is not the start of a line", offset)
		}
		data = data[1:]
	}
	validLen = offset
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
					return nil, 0, fmt.Errorf("corrupt event before byte %d: %w", offset+int64(lineEnd), jerr)
				}
				// Corrupt final line: treat as a torn tail and stop.
				break
			}
			events = append(events, e)
		}
		validLen = offset + int64(lineEnd+1)
		i = lineEnd + 1
	}
	return events, validLen, nil
}

// readFrom returns the contents of the file at path from byte offset to EOF.
// A missing file reads as empty.
func readFrom(path string, offset int64) ([]byte, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open ledger: %w", err)
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, errors.Join(fmt.Errorf("seek ledger: %w", err), f.Close())
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("read ledger: %w", err), f.Close())
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close ledger: %w", err)
	}
	return data, nil
}
