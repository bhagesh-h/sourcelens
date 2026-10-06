//go:build windows

package main

// The console on stdout, for the progress bar on Windows: escape sequences are
// switched on (ENABLE_VIRTUAL_TERMINAL_PROCESSING), as in python's enable_ansi.

import (
	"os"
	"strconv"
	"unsafe"
)

var (
	procGetConsoleMode             = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode             = kernel32.NewProc("SetConsoleMode")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
)

type consoleCoord struct{ x, y int16 }

type consoleInfo struct {
	size, cursor             consoleCoord
	attributes               uint16
	left, top, right, bottom int16
	maxWindow                consoleCoord
}

// stdoutTerminal reports whether stdout is a console that understands escape
// sequences, and its width (COLUMNS first, then the window, then 80).
func stdoutTerminal() (int, bool) {
	h := os.Stdout.Fd()
	var mode uint32
	if r, _, _ := procGetConsoleMode.Call(h, uintptr(unsafe.Pointer(&mode))); r == 0 {
		return 80, false
	}
	if r, _, _ := procSetConsoleMode.Call(h, uintptr(mode|0x0004)); r == 0 {
		return 80, false
	}
	width := 80
	var info consoleInfo
	if r, _, _ := procGetConsoleScreenBufferInfo.Call(h, uintptr(unsafe.Pointer(&info))); r != 0 && info.right > info.left {
		width = int(info.right-info.left) + 1
	}
	if c, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && c > 0 {
		width = c
	}
	return width, true
}
