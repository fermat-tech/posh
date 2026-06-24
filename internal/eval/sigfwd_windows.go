//go:build windows

package eval

import (
	"os/exec"
	"strconv"
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

// setBackgroundAttrs starts a background job in its own process group so the
// console's CTRL_C_EVENT is not delivered to it. Without this, pressing Ctrl+C
// to interrupt a foreground command would also kill background jobs that share
// the shell's process group.
func setBackgroundAttrs(c *exec.Cmd) {
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

// killProcessTree kills a process and all its descendants using taskkill /F /T.
// On Windows, killing a parent does not automatically kill its children, so
// grandchild processes (e.g. sleep inside a posh -c "..." job) would otherwise
// survive and keep I/O handles open, blocking cmd.Wait().
func killProcessTree(pid int) error {
	return exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run()
}
