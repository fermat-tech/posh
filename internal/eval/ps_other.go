//go:build !windows

package eval

import "io"

func builtinPs(_ *Shell, _ []string, _ io.Reader, _ io.Writer, stderr io.Writer) int {
	// On non-Windows, fall through to the system ps via PATH.
	return 127
}
