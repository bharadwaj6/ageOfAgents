//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package filelock

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// supported reports whether this platform really locks; the tests skip the
// exclusion checks where it does not.
const supported = true

func lock(f *os.File) error {
	return flock(f, "Lock", syscall.LOCK_EX)
}

func tryLock(f *os.File) (bool, error) {
	err := flock(f, "TryLock", syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func unlock(f *os.File) error {
	return flock(f, "Unlock", syscall.LOCK_UN)
}

// flock applies how to f, retrying when a signal interrupts a blocking wait.
func flock(f *os.File, op string, how int) error {
	for {
		err := syscall.Flock(int(f.Fd()), how)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return &fs.PathError{Op: op, Path: f.Name(), Err: err}
		}
		return nil
	}
}
