package main

// Vi-mode line editor.
// Platform-agnostic state machine; key reading and raw-mode setup are in
// viline_windows.go (ReadConsoleInputW — no escape-sequence parsing needed).

import (
	"fmt"
	"os"
	"strings"
)

// ---- types shared with platform file ----

type keyType int

const (
	keyRune      keyType = iota
	keyEnter
	keyEscape
	keyBackspace
	keyDelete
	keyLeft
	keyRight
	keyUp
	keyDown
	keyHome
	keyEnd
	keyCtrlA
	keyCtrlE
	keyCtrlU
	keyCtrlW
	keyCtrlK
	keyEOF
	keyInterrupt
)

type keyEvent struct {
	typ keyType
	r   rune
}

// ---- error sentinels ----

type viEOFError struct{}
type viInterruptError struct{}

func (viEOFError) Error() string       { return "EOF" }
func (viInterruptError) Error() string { return "interrupt" }

var errEOF = viEOFError{}
var errInterrupt = viInterruptError{}

func isViEOF(err error) bool       { _, ok := err.(viEOFError); return ok }
func isViInterrupt(err error) bool { _, ok := err.(viInterruptError); return ok }

// ---- vi editor mode ----

type viModeT int

const (
	viInsert viModeT = iota
	viNormal
)

type viState struct {
	buf            []rune
	pos            int
	mode           viModeT
	history        []string
	histIdx        int
	saved          []rune
	yank           []rune
	prompt         string
	lastDisplayLen int // visible columns written in previous redraw (for erase-to-end)
}

// viReadLine reads one line using the vi-mode editor.
// consoleRawMode and readKey are implemented in viline_windows.go.
func viReadLine(prompt string, history []string) (string, error) {
	restore, err := consoleRawMode()
	if err != nil {
		// fallback: plain input
		fmt.Fprint(os.Stdout, prompt)
		var line string
		fmt.Scanln(&line)
		return line, nil
	}
	defer restore()

	vs := &viState{
		prompt:  prompt,
		history: append([]string(nil), history...),
		histIdx: len(history),
		mode:    viInsert,
	}
	vs.redraw()

	for {
		key, err := readKey()
		if err != nil {
			vs.crlf()
			return string(vs.buf), err
		}

		var done bool
		var line string
		var rerr error

		if vs.mode == viInsert {
			done, line, rerr = vs.handleInsert(key)
		} else {
			done, line, rerr = vs.handleNormal(key)
		}
		if done {
			vs.crlf()
			return line, rerr
		}
	}
}

// ---- insert mode ----

func (vs *viState) handleInsert(key keyEvent) (done bool, line string, err error) {
	switch key.typ {
	case keyEnter:
		return true, string(vs.buf), nil
	case keyEOF:
		return true, "", errEOF
	case keyInterrupt:
		vs.buf = vs.buf[:0]
		vs.pos = 0
		return true, "", errInterrupt
	case keyEscape:
		vs.mode = viNormal
		if vs.pos > 0 {
			vs.pos--
		}
	case keyBackspace:
		if vs.pos > 0 {
			vs.buf = append(vs.buf[:vs.pos-1], vs.buf[vs.pos:]...)
			vs.pos--
		}
	case keyDelete:
		if vs.pos < len(vs.buf) {
			vs.buf = append(vs.buf[:vs.pos], vs.buf[vs.pos+1:]...)
		}
	case keyLeft:
		if vs.pos > 0 {
			vs.pos--
		}
	case keyRight:
		if vs.pos < len(vs.buf) {
			vs.pos++
		}
	case keyUp:
		vs.historyUp()
	case keyDown:
		vs.historyDown()
	case keyHome, keyCtrlA:
		vs.pos = 0
	case keyEnd, keyCtrlE:
		vs.pos = len(vs.buf)
	case keyCtrlU:
		vs.yank = append([]rune(nil), vs.buf[:vs.pos]...)
		vs.buf = append([]rune(nil), vs.buf[vs.pos:]...)
		vs.pos = 0
	case keyCtrlW:
		start := vs.wordBackPos()
		vs.yank = append([]rune(nil), vs.buf[start:vs.pos]...)
		vs.buf = append(vs.buf[:start], vs.buf[vs.pos:]...)
		vs.pos = start
	case keyCtrlK:
		vs.yank = append([]rune(nil), vs.buf[vs.pos:]...)
		vs.buf = vs.buf[:vs.pos]
	case keyRune:
		vs.buf = append(vs.buf[:vs.pos], append([]rune{key.r}, vs.buf[vs.pos:]...)...)
		vs.pos++
	}
	vs.redraw()
	return false, "", nil
}

// ---- normal mode ----

func (vs *viState) handleNormal(key keyEvent) (done bool, line string, err error) {
	switch key.typ {
	case keyEnter:
		return true, string(vs.buf), nil
	case keyEOF:
		return true, "", errEOF
	case keyInterrupt:
		vs.buf = vs.buf[:0]
		vs.pos = 0
		return true, "", errInterrupt
	case keyLeft:
		if vs.pos > 0 {
			vs.pos--
		}
	case keyRight:
		if vs.pos < len(vs.buf)-1 {
			vs.pos++
		}
	case keyUp:
		vs.historyUp()
	case keyDown:
		vs.historyDown()
	case keyHome:
		vs.pos = 0
	case keyEnd:
		if len(vs.buf) > 0 {
			vs.pos = len(vs.buf) - 1
		}
	case keyEscape:
		// already normal
	case keyRune:
		vs.handleNormalRune(key.r)
		vs.redraw()
		return false, "", nil
	}
	vs.redraw()
	return false, "", nil
}

func (vs *viState) handleNormalRune(r rune) {
	switch r {
	case 'h':
		if vs.pos > 0 {
			vs.pos--
		}
	case 'l':
		if vs.pos < len(vs.buf)-1 {
			vs.pos++
		}
	case 'w':
		vs.pos = vs.wordFwdPos()
	case 'b':
		vs.pos = vs.wordBackPos()
	case '0':
		vs.pos = 0
	case '$':
		if len(vs.buf) > 0 {
			vs.pos = len(vs.buf) - 1
		}
	case 'x':
		if vs.pos < len(vs.buf) {
			vs.yank = []rune{vs.buf[vs.pos]}
			vs.buf = append(vs.buf[:vs.pos], vs.buf[vs.pos+1:]...)
			if vs.pos >= len(vs.buf) && vs.pos > 0 {
				vs.pos = len(vs.buf) - 1
			}
		}
	case 'X':
		if vs.pos > 0 {
			vs.yank = []rune{vs.buf[vs.pos-1]}
			vs.buf = append(vs.buf[:vs.pos-1], vs.buf[vs.pos:]...)
			vs.pos--
		}
	case 'd':
		next, err := readKey()
		if err != nil {
			return
		}
		switch next.r {
		case 'd':
			vs.yank = append([]rune(nil), vs.buf...)
			vs.buf = vs.buf[:0]
			vs.pos = 0
		case 'w':
			end := vs.wordFwdPos()
			vs.yank = append([]rune(nil), vs.buf[vs.pos:end]...)
			vs.buf = append(vs.buf[:vs.pos], vs.buf[end:]...)
			if vs.pos >= len(vs.buf) && vs.pos > 0 {
				vs.pos = len(vs.buf) - 1
			}
		case 'b':
			start := vs.wordBackPos()
			vs.yank = append([]rune(nil), vs.buf[start:vs.pos]...)
			vs.buf = append(vs.buf[:start], vs.buf[vs.pos:]...)
			vs.pos = start
		case '$':
			vs.yank = append([]rune(nil), vs.buf[vs.pos:]...)
			vs.buf = vs.buf[:vs.pos]
			if vs.pos >= len(vs.buf) && vs.pos > 0 {
				vs.pos = len(vs.buf) - 1
			}
		}
	case 'D':
		vs.yank = append([]rune(nil), vs.buf[vs.pos:]...)
		vs.buf = vs.buf[:vs.pos]
		if vs.pos >= len(vs.buf) && vs.pos > 0 {
			vs.pos = len(vs.buf) - 1
		}
	case 'c':
		next, err := readKey()
		if err != nil {
			return
		}
		switch next.r {
		case 'c':
			vs.yank = append([]rune(nil), vs.buf...)
			vs.buf = vs.buf[:0]
			vs.pos = 0
		case 'w':
			end := vs.wordFwdPos()
			vs.yank = append([]rune(nil), vs.buf[vs.pos:end]...)
			vs.buf = append(vs.buf[:vs.pos], vs.buf[end:]...)
		case 'b':
			start := vs.wordBackPos()
			vs.yank = append([]rune(nil), vs.buf[start:vs.pos]...)
			vs.buf = append(vs.buf[:start], vs.buf[vs.pos:]...)
			vs.pos = start
		case '$':
			vs.yank = append([]rune(nil), vs.buf[vs.pos:]...)
			vs.buf = vs.buf[:vs.pos]
		}
		vs.mode = viInsert
	case 'C':
		vs.yank = append([]rune(nil), vs.buf[vs.pos:]...)
		vs.buf = vs.buf[:vs.pos]
		vs.mode = viInsert
	case 'r':
		next, err := readKey()
		if err != nil {
			return
		}
		if next.typ == keyRune && vs.pos < len(vs.buf) {
			vs.buf[vs.pos] = next.r
		}
	case 's':
		if vs.pos < len(vs.buf) {
			vs.yank = []rune{vs.buf[vs.pos]}
			vs.buf = append(vs.buf[:vs.pos], vs.buf[vs.pos+1:]...)
		}
		vs.mode = viInsert
	case 'i':
		vs.mode = viInsert
	case 'a':
		if vs.pos < len(vs.buf) {
			vs.pos++
		}
		vs.mode = viInsert
	case 'A':
		vs.pos = len(vs.buf)
		vs.mode = viInsert
	case 'I':
		vs.pos = 0
		vs.mode = viInsert
	case 'p':
		if len(vs.yank) > 0 {
			ins := vs.pos + 1
			if ins > len(vs.buf) {
				ins = len(vs.buf)
			}
			vs.buf = append(vs.buf[:ins], append(append([]rune(nil), vs.yank...), vs.buf[ins:]...)...)
			vs.pos = ins + len(vs.yank) - 1
		}
	case 'P':
		if len(vs.yank) > 0 {
			vs.buf = append(vs.buf[:vs.pos], append(append([]rune(nil), vs.yank...), vs.buf[vs.pos:]...)...)
			vs.pos += len(vs.yank) - 1
		}
	case 'k', '-':
		vs.historyUp()
	case 'j', '+':
		vs.historyDown()
	}
}

// ---- history ----

func (vs *viState) historyUp() {
	if vs.histIdx == len(vs.history) {
		vs.saved = append([]rune(nil), vs.buf...)
	}
	if vs.histIdx > 0 {
		vs.histIdx--
		vs.buf = []rune(vs.history[vs.histIdx])
		vs.pos = len(vs.buf)
		if vs.mode == viNormal && vs.pos > 0 {
			vs.pos--
		}
	}
}

func (vs *viState) historyDown() {
	if vs.histIdx < len(vs.history) {
		vs.histIdx++
		if vs.histIdx == len(vs.history) {
			vs.buf = append([]rune(nil), vs.saved...)
		} else {
			vs.buf = []rune(vs.history[vs.histIdx])
		}
		vs.pos = len(vs.buf)
		if vs.mode == viNormal && vs.pos > 0 {
			vs.pos--
		}
	}
}

// ---- word motion ----

func (vs *viState) wordFwdPos() int {
	i := vs.pos
	n := len(vs.buf)
	for i < n && !isViSpace(vs.buf[i]) {
		i++
	}
	for i < n && isViSpace(vs.buf[i]) {
		i++
	}
	return i
}

func (vs *viState) wordBackPos() int {
	i := vs.pos
	for i > 0 && isViSpace(vs.buf[i-1]) {
		i--
	}
	for i > 0 && !isViSpace(vs.buf[i-1]) {
		i--
	}
	return i
}

func isViSpace(r rune) bool { return r == ' ' || r == '\t' }

// ---- display ----

// visibleLen returns the display width of s, ignoring ANSI escape codes.
func visibleLen(s string) int {
	n := 0
	inEsc := false
	for _, r := range s {
		if inEsc {
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		if r == '\033' {
			inEsc = true
			continue
		}
		n++
	}
	return n
}

func (vs *viState) redraw() {
	promptVW := visibleLen(vs.prompt)
	currentLen := promptVW + len(vs.buf)

	var sb strings.Builder
	// Go to column 0 and rewrite the whole line.
	sb.WriteByte('\r')
	sb.WriteString(vs.prompt)
	sb.WriteString(string(vs.buf))

	// Erase leftover characters if the line got shorter since last redraw.
	for i := currentLen; i < vs.lastDisplayLen; i++ {
		sb.WriteByte(' ')
	}
	vs.lastDisplayLen = currentLen

	// Move cursor back to the correct position using backspaces.
	// After writing we are at column max(currentLen, lastDisplayLen);
	// we want to be at column (promptVW + pos).
	endCol := currentLen
	if vs.lastDisplayLen > endCol {
		endCol = vs.lastDisplayLen
	}
	target := promptVW + vs.pos
	for i := target; i < endCol; i++ {
		sb.WriteByte('\b')
	}

	fmt.Fprint(os.Stdout, sb.String())
}

func (vs *viState) crlf() { fmt.Fprint(os.Stdout, "\r\n") }
