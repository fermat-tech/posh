//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode             = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode             = kernel32.NewProc("SetConsoleMode")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
	procGetStdHandle               = kernel32.NewProc("GetStdHandle")
	procSetConsoleCP               = kernel32.NewProc("SetConsoleCP")
	procGetConsoleCP               = kernel32.NewProc("GetConsoleCP")
	procSetConsoleOutputCP         = kernel32.NewProc("SetConsoleOutputCP")
	procGetConsoleOutputCP         = kernel32.NewProc("GetConsoleOutputCP")
	procWaitForSingleObject        = kernel32.NewProc("WaitForSingleObject")
)

type viCoord struct{ x, y int16 }

type viConsoleScreenBufferInfo struct {
	size       viCoord
	cursorPos  viCoord
	attributes uint16
	window     [4]int16
	maxSize    viCoord
}

func stdoutHandle() syscall.Handle {
	h, _, _ := procGetStdHandle.Call(uintptr(0xFFFFFFF5)) // STD_OUTPUT_HANDLE
	return syscall.Handle(h)
}

// cursorColumn returns the current cursor column (0-based) from the console, so
// a prompt can be drawn at the cursor's actual column (e.g. after partial-line
// output) rather than assuming column 0.
func cursorColumn() int {
	var info viConsoleScreenBufferInfo
	r, _, _ := procGetConsoleScreenBufferInfo.Call(uintptr(stdoutHandle()), uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return 0
	}
	return int(info.cursorPos.x)
}

// terminalCols returns the console width in columns (visible window width),
// falling back to 80 when it cannot be determined.
func terminalCols() int {
	var info viConsoleScreenBufferInfo
	r, _, _ := procGetConsoleScreenBufferInfo.Call(uintptr(stdoutHandle()), uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return 80
	}
	// window is a SMALL_RECT: [Left, Top, Right, Bottom].
	w := int(info.window[2]) - int(info.window[0]) + 1
	if w <= 0 {
		return 80
	}
	return w
}

const (
	enableProcessedInput = 0x0001
	enableLineInput      = 0x0002
	enableEchoInput      = 0x0004
	enableInsertMode     = 0x0020
	enableExtendedFlags  = 0x0080
	enableVTInput        = 0x0200 // deliver keyboard events as UTF-8 VT sequences
	enableVTOutput       = 0x0004 // ENABLE_VIRTUAL_TERMINAL_PROCESSING (interpret ANSI on output)
)

// init switches the console to UTF-8 code pages (65001) and enables ANSI/VT
// processing on stdout at startup. The UTF-8 code pages make all shell output —
// not just the line-editing phase — render correctly; VT output processing lets
// the multi-row line editor's ANSI escape sequences be interpreted rather than
// printed literally. Called once; the original values are never restored because
// the shell operates this way for its whole lifetime.
func init() {
	procSetConsoleCP.Call(65001)
	procSetConsoleOutputCP.Call(65001)

	h := stdoutHandle()
	var mode uint32
	if r, _, _ := procGetConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r != 0 {
		procSetConsoleMode.Call(uintptr(h), uintptr(mode|enableVTOutput))
	}
}

// consoleRawMode puts the Windows console into raw mode (no echo, no line
// buffering) with VT sequence processing enabled. Code pages are already
// UTF-8 (set in init). Returns a restore function that only restores the
// console input mode, not the code pages.
func consoleRawMode() (restore func(), err error) {
	stdin := syscall.Handle(os.Stdin.Fd())

	var oldIn uint32
	r, _, e := procGetConsoleMode.Call(uintptr(stdin), uintptr(unsafe.Pointer(&oldIn)))
	if r == 0 {
		return nil, e
	}

	// Raw mode: no echo, no line buffering, no processed input.
	// Enable VT input so keystrokes arrive as UTF-8 / VT sequences via ReadFile.
	newIn := (oldIn &^ (enableProcessedInput | enableLineInput | enableEchoInput | enableInsertMode)) |
		enableExtendedFlags | enableVTInput
	r, _, e = procSetConsoleMode.Call(uintptr(stdin), uintptr(newIn))
	if r == 0 {
		return nil, e
	}

	return func() {
		procSetConsoleMode.Call(uintptr(stdin), uintptr(oldIn))
	}, nil
}

// readKey reads one logical key event from stdin as a UTF-8 byte stream.
// With ENABLE_VIRTUAL_TERMINAL_INPUT set, Windows delivers regular characters
// as UTF-8 bytes (including multi-byte emoji) and special keys as VT escape
// sequences — no UTF-16 surrogate handling needed.
func readKey() (keyEvent, error) {
	for {
		var b [1]byte
		_, err := os.Stdin.Read(b[:])
		if err != nil {
			return keyEvent{typ: keyEOF}, err
		}

		c := b[0]

		switch c {
		case 0x01:
			return keyEvent{typ: keyCtrlA}, nil
		case 0x03:
			return keyEvent{typ: keyInterrupt}, nil
		case 0x04:
			return keyEvent{typ: keyEOF}, nil
		case 0x05:
			return keyEvent{typ: keyCtrlE}, nil
		case 0x08, 0x7f:
			return keyEvent{typ: keyBackspace}, nil
		case 0x09:
			return keyEvent{typ: keyTab}, nil
		case 0x0a, 0x0d:
			return keyEvent{typ: keyEnter}, nil
		case 0x0b:
			return keyEvent{typ: keyCtrlK}, nil
		case 0x12:
			return keyEvent{typ: keyCtrlR}, nil
		case 0x13:
			return keyEvent{typ: keyCtrlS}, nil
		case 0x15:
			return keyEvent{typ: keyCtrlU}, nil
		case 0x17:
			return keyEvent{typ: keyCtrlW}, nil
		case 0x1b:
			return readEscapeSequence()
		default:
			if c >= 0x80 {
				r, err := readUTF8Rune(c)
				if err != nil {
					return keyEvent{typ: keyEOF}, err
				}
				return keyEvent{typ: keyRune, r: r}, nil
			}
			if c >= 0x20 {
				return keyEvent{typ: keyRune, r: rune(c)}, nil
			}
			// Skip other unhandled control bytes.
		}
	}
}

// readEscapeSequence reads and classifies a VT escape sequence after the
// leading ESC (0x1b) has already been consumed.
// Uses a short WaitForSingleObject timeout to distinguish ESC-alone from the
// start of a multi-byte sequence (e.g. arrow keys send ESC [ A).
func readEscapeSequence() (keyEvent, error) {
	stdin := syscall.Handle(os.Stdin.Fd())

	// Wait up to 50 ms for the next byte. If nothing arrives it was ESC alone.
	const waitTimeout = 50 // milliseconds
	ret, _, _ := procWaitForSingleObject.Call(uintptr(stdin), waitTimeout)
	if ret != 0 { // WAIT_TIMEOUT (0x102) or error
		return keyEvent{typ: keyEscape}, nil
	}

	var b [1]byte
	os.Stdin.Read(b[:])
	if b[0] != '[' && b[0] != 'O' {
		// Unrecognised sequence — consume and return ESC.
		return keyEvent{typ: keyEscape}, nil
	}

	os.Stdin.Read(b[:])
	switch b[0] {
	case 'A':
		return keyEvent{typ: keyUp}, nil
	case 'B':
		return keyEvent{typ: keyDown}, nil
	case 'C':
		return keyEvent{typ: keyRight}, nil
	case 'D':
		return keyEvent{typ: keyLeft}, nil
	case 'H':
		return keyEvent{typ: keyHome}, nil
	case 'F':
		return keyEvent{typ: keyEnd}, nil
	case '1': // ESC [ 1 ~ → Home
		os.Stdin.Read(b[:]) // consume ~
		return keyEvent{typ: keyHome}, nil
	case '3': // ESC [ 3 ~ → Delete
		os.Stdin.Read(b[:]) // consume ~
		return keyEvent{typ: keyDelete}, nil
	case '4': // ESC [ 4 ~ → End
		os.Stdin.Read(b[:]) // consume ~
		return keyEvent{typ: keyEnd}, nil
	}

	return keyEvent{typ: keyEscape}, nil
}
