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
	keyTab
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

// completeFn mirrors the liner WordCompleter signature.
type completeFn func(line string, pos int) (head string, completions []string, tail string)

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
	lastFChar      rune
	lastFForward   bool // true = f (forward), false = F (backward)
	lastFSet       bool
	completer      completeFn
	lastTabBuf     string // line at the last Tab press (for double-Tab detection)
	originCol      int    // cursor column where the prompt was first drawn
}

// viReadLine reads one line using the vi-mode editor.
// consoleRawMode and readKey are implemented in viline_windows.go.
func viReadLine(prompt string, history []string, completer completeFn) (string, error) {
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
		prompt:    prompt,
		history:   append([]string(nil), history...),
		histIdx:   len(history),
		mode:      viInsert,
		completer: completer,
		originCol: cursorColumn(),
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
	case keyTab:
		vs.doComplete()
		return false, "", nil
	case keyRune:
		vs.buf = append(vs.buf[:vs.pos], append([]rune{key.r}, vs.buf[vs.pos:]...)...)
		vs.pos++
	}
	vs.redraw()
	return false, "", nil
}

func (vs *viState) doComplete() {
	if vs.completer == nil {
		return
	}
	line := string(vs.buf)
	head, completions, _ := vs.completer(line, vs.pos)
	if len(completions) == 0 {
		return
	}

	if len(completions) == 1 {
		word := completions[0]
		if strings.Contains(word, " ") {
			word = `"` + word + `"`
		}
		newLine := head + word
		if !strings.HasSuffix(word, "/") && !strings.HasSuffix(word, "/\"") {
			newLine += " "
		}
		vs.buf = []rune(newLine)
		vs.pos = len(vs.buf)
		vs.lastTabBuf = ""
		vs.redraw()
		return
	}

	common := viCommonPrefix(completions)
	newLine := head + common
	showList := string(vs.buf) == vs.lastTabBuf // second Tab with no change → list
	vs.buf = []rune(newLine)
	vs.pos = len(vs.buf)
	vs.lastTabBuf = string(vs.buf)

	if showList || common == string([]rune(line)[len([]rune(head)):vs.pos]) {
		fmt.Fprintf(os.Stdout, "\r\n")
		for _, c := range completions {
			fmt.Fprintf(os.Stdout, "%s\r\n", c)
		}
		vs.lastDisplayLen = 0
	}
	vs.redraw()
}

func viCommonPrefix(strs []string) string {
	if len(strs) == 0 {
		return ""
	}
	prefix := strs[0]
	for _, s := range strs[1:] {
		for !strings.HasPrefix(strings.ToLower(s), strings.ToLower(prefix)) {
			if len(prefix) == 0 {
				return ""
			}
			prefix = prefix[:len(prefix)-1]
		}
	}
	return prefix
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
	case 'f':
		next, err := readKey()
		if err != nil || next.typ != keyRune {
			return
		}
		vs.lastFChar, vs.lastFForward, vs.lastFSet = next.r, true, true
		vs.findChar(next.r, true)
	case 'F':
		next, err := readKey()
		if err != nil || next.typ != keyRune {
			return
		}
		vs.lastFChar, vs.lastFForward, vs.lastFSet = next.r, false, true
		vs.findChar(next.r, false)
	case ';':
		if vs.lastFSet {
			vs.findChar(vs.lastFChar, vs.lastFForward)
		}
	case ',':
		if vs.lastFSet {
			vs.findChar(vs.lastFChar, !vs.lastFForward)
		}
	case 'k', '-':
		vs.historyUp()
	case 'j', '+':
		vs.historyDown()
	}
}

// ---- character search ----

func (vs *viState) findChar(ch rune, forward bool) {
	if forward {
		for i := vs.pos + 1; i < len(vs.buf); i++ {
			if vs.buf[i] == ch {
				vs.pos = i
				return
			}
		}
	} else {
		for i := vs.pos - 1; i >= 0; i-- {
			if vs.buf[i] == ch {
				vs.pos = i
				return
			}
		}
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

	// Return to the column where this prompt started, then rewrite.
	setCursorX(vs.originCol)

	var sb strings.Builder
	sb.WriteString(vs.prompt)
	sb.WriteString(string(vs.buf))

	// Erase leftover characters if the line got shorter since last redraw.
	// endCol is where the cursor actually sits after writing buf + erase spaces.
	endCol := currentLen
	if vs.lastDisplayLen > endCol {
		endCol = vs.lastDisplayLen
	}
	for i := currentLen; i < vs.lastDisplayLen; i++ {
		sb.WriteByte(' ')
	}
	vs.lastDisplayLen = currentLen

	// Move cursor back to the correct position using backspaces.
	target := promptVW + vs.pos
	for i := target; i < endCol; i++ {
		sb.WriteByte('\b')
	}

	fmt.Fprint(os.Stdout, sb.String())
}

func (vs *viState) crlf() { fmt.Fprint(os.Stdout, "\r\n") }
