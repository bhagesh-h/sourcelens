//go:build windows

package main

// File locks on Windows: one byte far past the end of the lock file, so the
// file stays readable; src/sourcelens/common/locks.py locks the same byte.

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

const (
	lockfileFailImmediately = 0x1
	lockfileExclusiveLock   = 0x2
	windowsLockOffset       = 1 << 30
)

func lockFile(f *os.File, block bool) error {
	flags := uintptr(lockfileExclusiveLock)
	if !block {
		flags |= lockfileFailImmediately
	}
	ol := &syscall.Overlapped{Offset: windowsLockOffset}
	if r, _, err := procLockFileEx.Call(f.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(ol))); r == 0 {
		return err
	}
	return nil
}

func unlockFile(f *os.File) {
	ol := &syscall.Overlapped{Offset: windowsLockOffset}
	_, _, _ = procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(ol)))
}
