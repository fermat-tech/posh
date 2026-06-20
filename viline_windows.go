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
	procReadConsoleInputW          = kernel32.NewProc("ReadConsoleInputW")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
	procSetConsoleCursorPosition   = kernel32.NewProc("SetConsoleCursorPosition")
	procGetStdHandle               = kernel32.NewProc("GetStdHandle")
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

// cursorColumn returns the current cursor X position (0-based).
func cursorColumn() int {
	var info viConsoleScreenBufferInfo
	procGetConsoleScreenBufferInfo.Call(uintptr(stdoutHandle()), uintptr(unsafe.Pointer(&info)))
	return int(info.cursorPos.x)
}

// setCursorX moves the cursor to column x on the current row (0-based).
func setCursorX(x int) {
	var info viConsoleScreenBufferInfo
	h := stdoutHandle()
	procGetConsoleScreenBufferInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&info)))
	pos := uint32(uint16(x)) | uint32(uint16(info.cursorPos.y))<<16
	procSetConsoleCursorPosition.Call(uintptr(h), uintptr(pos))
}

const (
	enableProcessedInput         = 0x0001 // when set, Ctrl+C → SIGINT; we clear it so it arrives as a key event
	enableLineInput              = 0x0002
	enableEchoInput              = 0x0004
	enableInsertMode             = 0x0020
	enableQuickEditMode          = 0x0040
	enableExtendedFlags          = 0x0080

	keyEventType = 0x0001

	vkBack    = 0x08
	vkTab     = 0x09
	vkReturn  = 0x0D
	vkEscape  = 0x1B
	vkEnd     = 0x23
	vkHome    = 0x24
	vkLeft    = 0x25
	vkUp      = 0x26
	vkRight   = 0x27
	vkDown    = 0x28
	vkDelete  = 0x2E

	rightCtrlPressed = 0x0004
	leftCtrlPressed  = 0x0008
)

// winKeyEventRecord mirrors Windows KEY_EVENT_RECORD.
type winKeyEventRecord struct {
	bKeyDown          int32
	wRepeatCount      uint16
	wVirtualKeyCode   uint16
	wVirtualScanCode  uint16
	uChar             uint16
	dwControlKeyState uint32
}

// winInputRecord mirrors Windows INPUT_RECORD.
type winInputRecord struct {
	eventType uint16
	_         [2]byte
	event     [16]byte
}

// consoleRawMode puts the Windows console into raw (non-buffered, no-echo) mode
// and enables VT processing on stdout. Returns a restore function.
func consoleRawMode() (restore func(), err error) {
	stdin := syscall.Handle(os.Stdin.Fd())

	var oldIn uint32
	r, _, e := procGetConsoleMode.Call(uintptr(stdin), uintptr(unsafe.Pointer(&oldIn)))
	if r == 0 {
		return nil, e
	}

	// Clear processed input so Ctrl+C arrives as a KEY_EVENT instead of SIGINT.
	// Keep ENABLE_QUICK_EDIT_MODE so SHIFT+right-click text selection still works.
	newIn := (oldIn &^ (enableProcessedInput | enableLineInput | enableEchoInput | enableInsertMode)) | enableExtendedFlags
	r, _, e = procSetConsoleMode.Call(uintptr(stdin), uintptr(newIn))
	if r == 0 {
		return nil, e
	}

	return func() {
		procSetConsoleMode.Call(uintptr(stdin), uintptr(oldIn))
	}, nil
}

// readKey reads one logical key event from the Windows console using
// ReadConsoleInputW. Delivers clean structured events with no escape-sequence
// parsing needed, eliminating the multi-byte split problem.
func readKey() (keyEvent, error) {
	stdin := syscall.Handle(os.Stdin.Fd())

	for {
		var rec winInputRecord
		var numRead uint32
		r, _, e := procReadConsoleInputW.Call(
			uintptr(stdin),
			uintptr(unsafe.Pointer(&rec)),
			1,
			uintptr(unsafe.Pointer(&numRead)),
		)
		if r == 0 {
			return keyEvent{typ: keyEOF}, e
		}
		if numRead == 0 || rec.eventType != keyEventType {
			continue
		}

		ke := (*winKeyEventRecord)(unsafe.Pointer(&rec.event[0]))
		if ke.bKeyDown == 0 {
			continue // ignore key-up events
		}

		ctrl := ke.dwControlKeyState&(rightCtrlPressed|leftCtrlPressed) != 0

		// Ctrl+key combinations via virtual key code
		if ctrl {
			switch ke.wVirtualKeyCode {
			case 'A':
				return keyEvent{typ: keyCtrlA}, nil
			case 'C':
				return keyEvent{typ: keyInterrupt}, nil
			case 'D':
				return keyEvent{typ: keyEOF}, nil
			case 'E':
				return keyEvent{typ: keyCtrlE}, nil
			case 'K':
				return keyEvent{typ: keyCtrlK}, nil
			case 'U':
				return keyEvent{typ: keyCtrlU}, nil
			case 'W':
				return keyEvent{typ: keyCtrlW}, nil
			}
		}

		switch ke.wVirtualKeyCode {
		case vkTab:
			return keyEvent{typ: keyTab}, nil
		case vkReturn:
			return keyEvent{typ: keyEnter}, nil
		case vkEscape:
			return keyEvent{typ: keyEscape}, nil
		case vkBack:
			return keyEvent{typ: keyBackspace}, nil
		case vkDelete:
			return keyEvent{typ: keyDelete}, nil
		case vkLeft:
			return keyEvent{typ: keyLeft}, nil
		case vkRight:
			return keyEvent{typ: keyRight}, nil
		case vkUp:
			return keyEvent{typ: keyUp}, nil
		case vkDown:
			return keyEvent{typ: keyDown}, nil
		case vkHome:
			return keyEvent{typ: keyHome}, nil
		case vkEnd:
			return keyEvent{typ: keyEnd}, nil
		}

		// Regular Unicode character
		ch := rune(ke.uChar)
		if ch >= 0x20 {
			return keyEvent{typ: keyRune, r: ch}, nil
		}
		// Skip unhandled control characters
	}
}
