//go:build !windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// consoleRawMode puts the terminal into raw mode and returns a restore func.
func consoleRawMode() (restore func(), err error) {
	fd := int(os.Stdin.Fd())
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	raw := *old
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
		unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &raw); err != nil {
		return nil, err
	}
	return func() {
		unix.IoctlSetTermios(fd, ioctlSetTermios, old)
	}, nil
}

// terminalCols returns the terminal width in columns, falling back to 80 when
// it cannot be determined (e.g. output is not a tty).
func terminalCols() int {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 {
		return 80
	}
	return int(ws.Col)
}

// cursorColumn queries the terminal for the current cursor column (0-based)
// using the ANSI Device Status Report: ESC[6n makes the terminal reply
// ESC[row;colR on stdin. Used once when a prompt starts, so the editor can draw
// it at the cursor's actual column (after partial-line output) instead of
// assuming column 0. Returns 0 if the terminal does not respond.
func cursorColumn() int {
	fmt.Fprint(os.Stdout, "\033[6n")
	var buf [32]byte
	n := 0
	b := make([]byte, 1)
	for n < len(buf) {
		nr, err := os.Stdin.Read(b)
		if err != nil || nr == 0 {
			break
		}
		buf[n] = b[0]
		n++
		if b[0] == 'R' {
			break
		}
	}
	col := 0
	inSeq := false
	sawSemi := false
	for i := 0; i < n; i++ {
		c := buf[i]
		if c == '[' {
			inSeq = true
			continue
		}
		if !inSeq {
			continue
		}
		if c == ';' {
			sawSemi = true
			col = 0
			continue
		}
		if c == 'R' {
			break
		}
		if c >= '0' && c <= '9' && sawSemi {
			col = col*10 + int(c-'0')
		}
	}
	if col > 0 {
		return col - 1 // 1-based to 0-based
	}
	return 0
}

// readKey reads one logical key event from stdin.
func readKey() (keyEvent, error) {
	b := make([]byte, 1)
	for {
		_, err := os.Stdin.Read(b)
		if err != nil {
			return keyEvent{typ: keyEOF}, err
		}
		ch := b[0]

		// Ctrl+C
		if ch == 0x03 {
			return keyEvent{typ: keyInterrupt}, nil
		}
		// Ctrl+D
		if ch == 0x04 {
			return keyEvent{typ: keyEOF}, nil
		}
		// Enter
		if ch == '\r' || ch == '\n' {
			return keyEvent{typ: keyEnter}, nil
		}
		// Backspace (0x7f DEL or 0x08 BS)
		if ch == 0x7f || ch == 0x08 {
			return keyEvent{typ: keyBackspace}, nil
		}
		// Tab
		if ch == '\t' {
			return keyEvent{typ: keyTab}, nil
		}
		// Escape or escape sequence
		if ch == 0x1b {
			// Try to read more bytes (may be escape sequence)
			buf := make([]byte, 8)
			// Non-blocking peek: set VMIN=0 VTIME=1 (100ms), read, restore
			n := readEscapeSeq(buf)
			if n == 0 {
				return keyEvent{typ: keyEscape}, nil
			}
			if buf[0] == '[' {
				switch {
				case n >= 2 && buf[1] == 'A':
					return keyEvent{typ: keyUp}, nil
				case n >= 2 && buf[1] == 'B':
					return keyEvent{typ: keyDown}, nil
				case n >= 2 && buf[1] == 'C':
					return keyEvent{typ: keyRight}, nil
				case n >= 2 && buf[1] == 'D':
					return keyEvent{typ: keyLeft}, nil
				case n >= 2 && buf[1] == 'H':
					return keyEvent{typ: keyHome}, nil
				case n >= 2 && buf[1] == 'F':
					return keyEvent{typ: keyEnd}, nil
				case n >= 3 && buf[1] == '1' && buf[2] == '~':
					return keyEvent{typ: keyHome}, nil
				case n >= 3 && buf[1] == '3' && buf[2] == '~':
					return keyEvent{typ: keyDelete}, nil
				case n >= 3 && buf[1] == '4' && buf[2] == '~':
					return keyEvent{typ: keyEnd}, nil
				case n >= 3 && buf[1] == '7' && buf[2] == '~':
					return keyEvent{typ: keyHome}, nil
				case n >= 3 && buf[1] == '8' && buf[2] == '~':
					return keyEvent{typ: keyEnd}, nil
				}
			} else if buf[0] == 'O' {
				switch {
				case n >= 2 && buf[1] == 'H':
					return keyEvent{typ: keyHome}, nil
				case n >= 2 && buf[1] == 'F':
					return keyEvent{typ: keyEnd}, nil
				case n >= 2 && buf[1] == 'A':
					return keyEvent{typ: keyUp}, nil
				case n >= 2 && buf[1] == 'B':
					return keyEvent{typ: keyDown}, nil
				case n >= 2 && buf[1] == 'C':
					return keyEvent{typ: keyRight}, nil
				case n >= 2 && buf[1] == 'D':
					return keyEvent{typ: keyLeft}, nil
				}
			}
			// Unrecognised escape sequence — discard
			continue
		}
		// Ctrl+A
		if ch == 0x01 {
			return keyEvent{typ: keyCtrlA}, nil
		}
		// Ctrl+E
		if ch == 0x05 {
			return keyEvent{typ: keyCtrlE}, nil
		}
		// Ctrl+K
		if ch == 0x0b {
			return keyEvent{typ: keyCtrlK}, nil
		}
		// Ctrl+U
		if ch == 0x15 {
			return keyEvent{typ: keyCtrlU}, nil
		}
		// Ctrl+W
		if ch == 0x17 {
			return keyEvent{typ: keyCtrlW}, nil
		}
		// Multi-byte UTF-8 (accented chars, emoji, etc.)
		if ch >= 0x80 {
			r, err := readUTF8Rune(ch)
			if err != nil {
				return keyEvent{typ: keyEOF}, err
			}
			return keyEvent{typ: keyRune, r: r}, nil
		}
		// Regular ASCII printable character
		if ch >= 0x20 {
			return keyEvent{typ: keyRune, r: rune(ch)}, nil
		}
		// Skip other control chars
	}
}

// readEscapeSeq reads additional bytes after ESC using a short timeout.
// Returns the number of bytes read into buf.
func readEscapeSeq(buf []byte) int {
	fd := int(os.Stdin.Fd())
	// Temporarily set VMIN=0, VTIME=1 (deciseconds) for non-blocking read.
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return 0
	}
	t := *old
	t.Cc[unix.VMIN] = 0
	t.Cc[unix.VTIME] = 1
	unix.IoctlSetTermios(fd, ioctlSetTermios, &t)
	defer unix.IoctlSetTermios(fd, ioctlSetTermios, old)

	n := 0
	b := make([]byte, 1)
	for n < len(buf) {
		nr, err := os.Stdin.Read(b)
		if err != nil || nr == 0 {
			break
		}
		buf[n] = b[0]
		n++
		// Stop at sequence terminators
		if b[0] == '~' || (b[0] >= 'A' && b[0] <= 'Z') || (b[0] >= 'a' && b[0] <= 'z') {
			break
		}
	}
	return n
}
