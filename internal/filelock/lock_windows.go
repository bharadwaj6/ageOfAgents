//go:build windows

package filelock

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

// supported reports whether this platform really locks; the tests skip the
// exclusion checks where it does not.
const supported = true

const (
	reserved = 0
	allBytes = ^uint32(0) // lock the whole file: low and high halves of the range length
)

func lock(f *os.File) error {
	return lockFileEx(f, "Lock", windows.LOCKFILE_EXCLUSIVE_LOCK)
}

func tryLock(f *os.File) (bool, error) {
	err := lockFileEx(f, "TryLock", windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// lockFileEx locks the whole of f. os.OpenFile returns a handle for ordinary
// synchronous I/O, so LockFileEx blocks until it is granted (unless flags ask it
// to fail immediately); the OVERLAPPED only carries the range's start offset,
// which is zero.
func lockFileEx(f *os.File, op string, flags uint32) error {
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), flags, reserved, allBytes, allBytes, ol); err != nil {
		return &fs.PathError{Op: op, Path: f.Name(), Err: err}
	}
	return nil
}

func unlock(f *os.File) error {
	ol := new(windows.Overlapped)
	if err := windows.UnlockFileEx(windows.Handle(f.Fd()), reserved, allBytes, allBytes, ol); err != nil {
		return &fs.PathError{Op: "Unlock", Path: f.Name(), Err: err}
	}
	return nil
}
