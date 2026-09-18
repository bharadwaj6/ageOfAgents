package filelock

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// openHandle opens path as a fresh handle — a separate open file description,
// which is what flock and LockFileEx lock — and closes it when the test ends.
func openHandle(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Errorf("close %s: %v", path, err)
		}
	})
	return f
}

func skipUnsupported(t *testing.T) {
	t.Helper()
	if !supported {
		t.Skip("file locking is a no-op on this platform")
	}
}

func TestTryLockFailsWhileHeld(t *testing.T) {
	skipUnsupported(t)
	path := filepath.Join(t.TempDir(), "x.lock")
	first, second := openHandle(t, path), openHandle(t, path)

	if err := Lock(first); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	ok, err := TryLock(second)
	if err != nil {
		t.Fatalf("TryLock while held: %v", err)
	}
	if ok {
		t.Fatal("TryLock succeeded on a second handle while the first held the lock")
	}

	if err := Unlock(first); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	ok, err = TryLock(second)
	if err != nil {
		t.Fatalf("TryLock after Unlock: %v", err)
	}
	if !ok {
		t.Fatal("TryLock failed after the holder unlocked")
	}
	if err := Unlock(second); err != nil {
		t.Fatalf("Unlock second: %v", err)
	}
}

// A process that dies holding the lock must not wedge every later caller:
// closing the handle, as the OS does on exit, releases it.
func TestLockReleasedOnClose(t *testing.T) {
	skipUnsupported(t)
	path := filepath.Join(t.TempDir(), "x.lock")
	holder, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open holder: %v", err)
	}
	if err := Lock(holder); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := holder.Close(); err != nil {
		t.Fatalf("close holder without Unlock: %v", err)
	}

	other := openHandle(t, path)
	// Windows documents that the release on close happens "when resources
	// allow", so give it a generous deadline rather than asserting at once.
	deadline := time.Now().Add(5 * time.Second)
	for {
		ok, err := TryLock(other)
		if err != nil {
			t.Fatalf("TryLock after close: %v", err)
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lock still held after its only handle was closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := Unlock(other); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
}
