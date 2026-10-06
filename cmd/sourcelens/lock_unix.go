//go:build !windows

package main

// File locks on Linux and macOS: flock, as python's fcntl.flock
// (src/sourcelens/common/locks.py), so the two implementations exclude each other.

import (
	"os"
	"syscall"
)

func lockFile(f *os.File, block bool) error {
	how := syscall.LOCK_EX
	if !block {
		how |= syscall.LOCK_NB
	}
	return syscall.Flock(int(f.Fd()), how)
}

func unlockFile(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
