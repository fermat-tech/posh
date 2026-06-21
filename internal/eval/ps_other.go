//go:build !windows && !linux

package eval

import (
	"io"
	"os/exec"
)

// On non-Linux Unix (e.g. macOS), exec the system ps directly.
func builtinPs(_ *Shell, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	path, found := lookupCommand("ps")
	if !found {
		path = "ps"
	}
	c := exec.Command(path, args...)
	c.Stdin = stdin
	c.Stdout = stdout
	c.Stderr = stderr
	if err := c.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}
