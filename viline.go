package main

// Vi-mode line editor.
// Platform-agnostic state machine; key reading and raw-mode setup are in
// viline_windows.go (ReadConsoleInputW — no escape-sequence parsing needed).

import (
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
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

type undoEntry struct {
	buf []rune
	pos int
}

type viState struct {
	buf          []rune
	pos          int
	mode         viModeT
	history      []string
	histIdx      int
	saved        []rune
	yank         []rune
	undoStack    []undoEntry
	prompt       string
	lastFChar    rune
	lastFForward bool // true = f (forward), false = F (backward)
	lastFSet     bool
	completer    completeFn
	lastTabBuf   string // line at the last Tab press (for double-Tab detection)

	// Multi-row redraw state (linenoise-style refresh). The editor renders the
	// prompt + buffer as a block that may span several terminal rows when the
	// input wraps; these track the previous render so the next one can clear and
	// reposition correctly across rows.
	maxRows      int // most rows the rendered block has occupied so far
	prevCursorDW int // cursor's display column (incl. prompt) at the last redraw
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
	}
	vs.redraw()

	for {
		key, err := readKey()
		if err != nil {
			vs.endLine()
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
			vs.endLine()
			return line, rerr
		}
	}
}

// endLine moves the cursor to the end of the rendered block and emits a newline,
// so submitting a multi-row line leaves the terminal on a fresh row below the
// whole input rather than wherever the cursor happened to be mid-buffer.
func (vs *viState) endLine() {
	vs.pos = len(vs.buf)
	vs.redraw()
	vs.crlf()
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
			vs.saveUndo()
			vs.buf = append(vs.buf[:vs.pos-1], vs.buf[vs.pos:]...)
			vs.pos--
		}
	case keyDelete:
		if vs.pos < len(vs.buf) {
			vs.saveUndo()
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
		vs.saveUndo()
		vs.yank = append([]rune(nil), vs.buf[:vs.pos]...)
		vs.buf = append([]rune(nil), vs.buf[vs.pos:]...)
		vs.pos = 0
	case keyCtrlW:
		start := vs.wordBackPos()
		if start < vs.pos {
			vs.saveUndo()
			vs.yank = append([]rune(nil), vs.buf[start:vs.pos]...)
			vs.buf = append(vs.buf[:start], vs.buf[vs.pos:]...)
			vs.pos = start
		}
	case keyCtrlK:
		if vs.pos < len(vs.buf) {
			vs.saveUndo()
			vs.yank = append([]rune(nil), vs.buf[vs.pos:]...)
			vs.buf = vs.buf[:vs.pos]
		}
	case keyTab:
		vs.doComplete()
		return false, "", nil
	case keyRune:
		vs.saveUndo()
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
	origPos := vs.pos // cursor position before completion mutates vs.pos/vs.buf
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

	// The fragment originally typed for this word: the runes of the original
	// line between the end of head and the original cursor. Compute it before
	// overwriting vs.pos/vs.buf, and bound it defensively — completions usually
	// extend the word, so the new cursor would otherwise index past the old line.
	lineRunes := []rune(line)
	headLen := len([]rune(head))
	typed := ""
	if headLen <= origPos && origPos <= len(lineRunes) {
		typed = string(lineRunes[headLen:origPos])
	}

	vs.buf = []rune(newLine)
	vs.pos = len(vs.buf)
	vs.lastTabBuf = string(vs.buf)

	if showList || common == typed {
		fmt.Fprintf(os.Stdout, "\r\n")
		for _, c := range completions {
			fmt.Fprintf(os.Stdout, "%s\r\n", c)
		}
		// The previous rendered block has scrolled away above the list; start
		// the next redraw from a clean slate on the current row.
		vs.resetRenderState()
	}
	vs.redraw()
}

// resetRenderState clears the multi-row redraw bookkeeping. Call it after
// emitting output (e.g. a completion list) that leaves the cursor on a fresh row
// with no live rendered block above it.
func (vs *viState) resetRenderState() {
	vs.maxRows = 0
	vs.prevCursorDW = 0
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

func (vs *viState) saveUndo() {
	vs.undoStack = append(vs.undoStack, undoEntry{
		buf: append([]rune(nil), vs.buf...),
		pos: vs.pos,
	})
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
			vs.saveUndo()
			vs.yank = []rune{vs.buf[vs.pos]}
			vs.buf = append(vs.buf[:vs.pos], vs.buf[vs.pos+1:]...)
			if vs.pos >= len(vs.buf) && vs.pos > 0 {
				vs.pos = len(vs.buf) - 1
			}
		}
	case 'X':
		if vs.pos > 0 {
			vs.saveUndo()
			vs.yank = []rune{vs.buf[vs.pos-1]}
			vs.buf = append(vs.buf[:vs.pos-1], vs.buf[vs.pos:]...)
			vs.pos--
		}
	case 'd':
		next, err := readKey()
		if err != nil || next.typ != keyRune {
			return
		}
		switch next.r {
		case 'd':
			vs.saveUndo()
			vs.yank = append([]rune(nil), vs.buf...)
			vs.buf = vs.buf[:0]
			vs.pos = 0
		case 'w':
			end := vs.wordFwdPos()
			if end == vs.pos {
				return
			}
			vs.saveUndo()
			vs.yank = append([]rune(nil), vs.buf[vs.pos:end]...)
			vs.buf = append(vs.buf[:vs.pos], vs.buf[end:]...)
			if vs.pos >= len(vs.buf) && vs.pos > 0 {
				vs.pos = len(vs.buf) - 1
			}
		case 'b':
			start := vs.wordBackPos()
			if start == vs.pos {
				return
			}
			vs.saveUndo()
			vs.yank = append([]rune(nil), vs.buf[start:vs.pos]...)
			vs.buf = append(vs.buf[:start], vs.buf[vs.pos:]...)
			vs.pos = start
		case '$':
			if vs.pos >= len(vs.buf) {
				return
			}
			vs.saveUndo()
			vs.yank = append([]rune(nil), vs.buf[vs.pos:]...)
			vs.buf = vs.buf[:vs.pos]
			if vs.pos >= len(vs.buf) && vs.pos > 0 {
				vs.pos = len(vs.buf) - 1
			}
		}
	case 'D':
		if vs.pos < len(vs.buf) {
			vs.saveUndo()
			vs.yank = append([]rune(nil), vs.buf[vs.pos:]...)
			vs.buf = vs.buf[:vs.pos]
			if vs.pos >= len(vs.buf) && vs.pos > 0 {
				vs.pos = len(vs.buf) - 1
			}
		}
	case 'c':
		next, err := readKey()
		if err != nil || next.typ != keyRune {
			return
		}
		switch next.r {
		case 'c':
			vs.saveUndo()
			vs.yank = append([]rune(nil), vs.buf...)
			vs.buf = vs.buf[:0]
			vs.pos = 0
		case 'w':
			end := vs.wordFwdPos()
			if end == vs.pos {
				vs.mode = viInsert
				return
			}
			vs.saveUndo()
			vs.yank = append([]rune(nil), vs.buf[vs.pos:end]...)
			vs.buf = append(vs.buf[:vs.pos], vs.buf[end:]...)
			if vs.pos > len(vs.buf) {
				vs.pos = len(vs.buf)
			}
		case 'b':
			start := vs.wordBackPos()
			if start == vs.pos {
				vs.mode = viInsert
				return
			}
			vs.saveUndo()
			vs.yank = append([]rune(nil), vs.buf[start:vs.pos]...)
			vs.buf = append(vs.buf[:start], vs.buf[vs.pos:]...)
			vs.pos = start
		case '$':
			vs.saveUndo()
			vs.yank = append([]rune(nil), vs.buf[vs.pos:]...)
			vs.buf = vs.buf[:vs.pos]
		}
		vs.mode = viInsert
	case 'C':
		vs.saveUndo()
		vs.yank = append([]rune(nil), vs.buf[vs.pos:]...)
		vs.buf = vs.buf[:vs.pos]
		vs.mode = viInsert
	case 'r':
		next, err := readKey()
		if err != nil {
			return
		}
		if next.typ == keyRune && vs.pos < len(vs.buf) {
			vs.saveUndo()
			vs.buf[vs.pos] = next.r
		}
	case 's':
		if vs.pos < len(vs.buf) {
			vs.saveUndo()
			vs.yank = []rune{vs.buf[vs.pos]}
			vs.buf = append(vs.buf[:vs.pos], vs.buf[vs.pos+1:]...)
		}
		vs.mode = viInsert
	case 'u':
		if len(vs.undoStack) > 0 {
			top := vs.undoStack[len(vs.undoStack)-1]
			vs.undoStack = vs.undoStack[:len(vs.undoStack)-1]
			vs.buf = top.buf
			vs.pos = top.pos
			if vs.pos >= len(vs.buf) && len(vs.buf) > 0 {
				vs.pos = len(vs.buf) - 1
			}
		}
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
		vs.pos = 0
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
		vs.pos = 0
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

// readUTF8Rune reads the continuation bytes of a multi-byte UTF-8 sequence
// whose leading byte is already in `first` and returns the decoded rune.
// Used by platform readKey implementations; stdin must be in raw mode.
func readUTF8Rune(first byte) (rune, error) {
	var total int
	switch {
	case first&0xE0 == 0xC0:
		total = 2
	case first&0xF0 == 0xE0:
		total = 3
	case first&0xF8 == 0xF0:
		total = 4
	default:
		return unicode.ReplacementChar, nil
	}
	buf := make([]byte, total)
	buf[0] = first
	for i := 1; i < total; i++ {
		var b [1]byte
		_, err := os.Stdin.Read(b[:])
		if err != nil {
			return 0, err
		}
		buf[i] = b[0]
	}
	r, _ := utf8.DecodeRune(buf)
	if r == utf8.RuneError {
		return unicode.ReplacementChar, nil
	}
	return r, nil
}

// ---- display ----

// visibleLen returns the display column width of s, ignoring ANSI escape
// codes and using proper widths for wide characters (emoji, CJK, etc.).
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
		n += runewidth.RuneWidth(r)
	}
	return n
}

// bufDisplayWidth returns the display column width of a rune slice.
func bufDisplayWidth(buf []rune) int {
	w := 0
	for _, r := range buf {
		w += runewidth.RuneWidth(r)
	}
	return w
}

func (vs *viState) crlf() { fmt.Fprint(os.Stdout, "\r\n") }

// redraw repaints the prompt and buffer, correctly handling input that wraps
// across several terminal rows, then writes the result to the terminal.
func (vs *viState) redraw() {
	cols := terminalCols()
	if cols < 1 {
		cols = 80
	}
	out, maxRows, cursorDW := renderLine(vs.prompt, vs.buf, vs.pos, cols, vs.maxRows, vs.prevCursorDW)
	vs.maxRows = maxRows
	vs.prevCursorDW = cursorDW
	fmt.Fprint(os.Stdout, out)
}

// renderLine produces the terminal output that repaints prompt+buf with the
// cursor at pos, given the terminal width and the previous render's row count
// and cursor display-column. It is the linenoise multi-line refresh algorithm:
// relative cursor movement (scroll-safe) clears the previous render from the
// bottom row upward, rewrites prompt+buffer, then moves the cursor to its target
// row and column. All positioning uses display widths so wide characters
// (CJK, emoji) line up. It returns the output string plus the updated maxRows
// and cursor display-column to store for the next call. Pure (no I/O) so the
// row arithmetic can be tested directly.
func renderLine(prompt string, buf []rune, pos, cols, oldMaxRows, prevCursorDW int) (out string, maxRows, cursorDW int) {
	plen := visibleLen(prompt)
	contentW := bufDisplayWidth(buf)
	posW := bufDisplayWidth(buf[:pos])

	rows := (plen + contentW + cols - 1) / cols
	if rows < 1 {
		rows = 1
	}
	prevCursorRow := (plen + prevCursorDW + cols) / cols // 1-based row of cursor last time

	var sb strings.Builder

	// 1. Move down to the last row of the previous render.
	if d := oldMaxRows - prevCursorRow; d > 0 {
		fmt.Fprintf(&sb, "\x1b[%dB", d)
	}
	// 2. Clear each previous row from the bottom up to (but not including) the top.
	for i := 0; i < oldMaxRows-1; i++ {
		sb.WriteString("\r\x1b[0K\x1b[1A")
	}
	// 3. Clear the top row.
	sb.WriteString("\r\x1b[0K")

	// 4. Rewrite prompt and buffer.
	sb.WriteString(prompt)
	sb.WriteString(string(buf))

	// 5. If the cursor is at end-of-buffer and exactly fills the last row, emit a
	//    newline so the cursor rests on a fresh row instead of hanging off the
	//    right edge (terminals defer the wrap until the next character).
	if pos == len(buf) && (plen+contentW) > 0 && (plen+contentW)%cols == 0 {
		sb.WriteString("\r\n")
		rows++
	}
	maxRows = oldMaxRows
	if rows > maxRows {
		maxRows = rows
	}

	// 6. Move the cursor up to its target row, then set its column.
	cursorRow := (plen + posW + cols) / cols // 1-based row of cursor now
	if up := rows - cursorRow; up > 0 {
		fmt.Fprintf(&sb, "\x1b[%dA", up)
	}
	if col := (plen + posW) % cols; col > 0 {
		fmt.Fprintf(&sb, "\r\x1b[%dC", col)
	} else {
		sb.WriteString("\r")
	}

	return sb.String(), maxRows, posW
}
