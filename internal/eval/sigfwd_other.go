//go:build !windows

package eval

import (
	"errors"
	"os/exec"
	"syscall"
)

func setForegroundAttrs(_ *exec.Cmd) {}

// setBackgroundAttrs puts a background job in its own process group so a
// terminal-generated SIGINT (Ctrl+C) aimed at the foreground command is not
// delivered to it.
func setBackgroundAttrs(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func sendInterrupt(pid int) {
	syscall.Kill(pid, syscall.SIGINT)
}

// killProcessTree is a no-op on Unix; returning an error causes the caller
// to fall back to j.Cmd.Process.Signal(sig) which is the correct Unix path.
func killProcessTree(pid int) error { return errors.New("use direct signal") }
