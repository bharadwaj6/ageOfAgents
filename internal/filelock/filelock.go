// Package filelock provides an exclusive whole-file lock that excludes other
// processes, and other open handles to the same file within one process.
//
// It is laid out like Go's cmd/go/internal/lockedfile/internal/filelock, cut
// down to the one lock aoa needs. On Unix it uses flock(2), whose locks belong
// to the open file description rather than the process: two separate
// [os.OpenFile] handles on one path exclude each other even in a single
// process, and the lock is released when the file is closed or the process
// exits. (fcntl locks would be silently shared by every handle in a process.)
// On Windows it uses LockFileEx.
//
// Windows locks are mandatory, not advisory: while one handle holds the lock,
// reads and writes of the file through any other handle fail. Lock a dedicated
// sidecar file that nothing reads, never the data file it protects, so that
// lock-free readers keep working on every platform.
//
// flock is unreliable on network filesystems such as NFS and on some container
// bind mounts. On platforms other than the BSDs, Linux, macOS and Windows the
// functions are no-ops (see lock_other.go).
package filelock

import "os"

// Lock blocks until it holds an exclusive lock on f. The lock is released by
// [Unlock], or by closing f.
func Lock(f *os.File) error { return lock(f) }

// TryLock attempts to take an exclusive lock on f without blocking. It returns
// false and a nil error when the lock is held through another handle.
func TryLock(f *os.File) (bool, error) { return tryLock(f) }

// Unlock releases a lock taken on f by [Lock] or [TryLock].
func Unlock(f *os.File) error { return unlock(f) }
