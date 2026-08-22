//go:build unix

package storage

import (
	"os"

	"golang.org/x/sys/unix"
)

// LockSupported reports whether cross-process file locking is available.
const LockSupported = true

// lockFile takes an exclusive advisory lock on an open file, blocking until it
// is granted.
//
// flock(2) locks the open file description, not the process, so two goroutines
// that each opened the path genuinely contend — unlike fcntl record locks,
// which are per-process and would be silently useless inside one daemon.
func lockFile(f *os.File) error {
	return flock(f, unix.LOCK_EX)
}

// tryLockFile takes the lock only if it is free, reporting whether it got it.
func tryLockFile(f *os.File) (bool, error) {
	err := flock(f, unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if err == unix.EWOULDBLOCK {
		return false, nil
	}

	return false, err
}

func unlockFile(f *os.File) error {
	return flock(f, unix.LOCK_UN)
}

// flock reaches the descriptor through SyscallConn rather than File.Fd(),
// which would switch the descriptor to blocking mode and drop it out of the
// runtime poller.
func flock(f *os.File, how int) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}

	var opErr error
	if err := rc.Control(func(fd uintptr) {
		opErr = unix.Flock(int(fd), how)
	}); err != nil {
		return err
	}

	return opErr
}
