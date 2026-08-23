//go:build !unix

package storage

import "os"

// LockSupported reports whether cross-process file locking is available.
//
// It is false here, so the in-process mutex is the only guard. That is enough
// for one daemon on its own; two processes sharing a var directory — the `gc`
// subcommand run by hand, or a still-running PHP install during a migration —
// can then undercount a quota. serve logs this once at startup.
const LockSupported = false

func lockFile(*os.File) error            { return nil }
func tryLockFile(*os.File) (bool, error) { return true, nil }
func unlockFile(*os.File) error          { return nil }
