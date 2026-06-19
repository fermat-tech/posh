//go:build windows

package eval

import (
	"os/exec"
	"syscall"
)

var (
	modKernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procGenerateConsoleCtrlEvent = modKernel32.NewProc("GenerateConsoleCtrlEvent")
)

const ctrlBreakEvent = 1 // CTRL_BREAK_EVENT

// setForegroundAttrs puts the child in its own process group so the shell can
// forward Ctrl+C explicitly rather than having the console deliver it to both.
func setForegroundAttrs(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

// sendInterrupt sends CTRL_BREAK_EVENT to the child's process group.
// CTRL_C_EVENT cannot be targeted at a specific process group on Windows;
// CTRL_BREAK_EVENT can, and causes the same default action (termination).
func sendInterrupt(pid int) {
	procGenerateConsoleCtrlEvent.Call(ctrlBreakEvent, uintptr(pid))
}
