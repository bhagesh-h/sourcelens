package main

// The terminal on stdout, for the progress bar (Linux and macOS), mirroring
// sys.stdout.isatty() and shutil.get_terminal_size() in python.

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

type winsize struct{ row, col, x, y uint16 }

// stdoutTerminal reports whether stdout is a terminal, and its width
// (COLUMNS first, as python does, then the terminal, then 80).
func stdoutTerminal() (int, bool) {
	var ws winsize
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if e != 0 {
		return 80, false
	}
	width := int(ws.col)
	if c, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && c > 0 {
		width = c
	}
	if width <= 0 {
		width = 80
	}
	return width, true
}
