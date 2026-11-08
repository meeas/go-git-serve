//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableVTOutput turns on escape-sequence processing for the console, which
// the line editor uses to move the cursor. Windows Terminal already has it;
// the classic console needs it switched on.
func enableVTOutput() {
	h := windows.Handle(os.Stderr.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) == nil {
		_ = windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	}
}
