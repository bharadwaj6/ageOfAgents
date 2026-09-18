//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows)

package filelock

import "os"

// supported reports whether this platform really locks. It does not here.
const supported = false

// File locking is unsupported on this platform (wasm, plan9, and Unixes whose
// syscall package has no flock). The functions succeed without locking, so a
// caller keeps whatever in-process exclusion it has and loses only the
// cross-process guarantee.

func lock(*os.File) error { return nil }

func tryLock(*os.File) (bool, error) { return true, nil }

func unlock(*os.File) error { return nil }
