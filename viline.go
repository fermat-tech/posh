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
	keyRune keyType = iota
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
	keyCtrlR
	keyCtrlS
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

	// History search. Two styles share this state:
	//   - vi (/ ? n N): non-incremental. The line becomes "/pattern"; the match
	//     is jumped to on Enter, and n/N repeat it afterward.
	//   - emacs (Ctrl+R / Ctrl+S): incremental. A "(reverse-i-search)`pat':"
	//     prompt is shown and the match is previewed live as the pattern changes.
	// searchOlder is the direction; searchLast remembers the pattern for n/N.
	searching      bool
	searchVi       bool
	searchInput    []rune
	searchOlder    bool
	searchLast     string
	searchOrig     []rune  // buffer to restore if the search is cancelled
	searchPrevMode viModeT // editor mode to return to when the search ends

	// Multi-line editing. The buffer is a single logical command that may contain
	// embedded newlines (added when Enter is pressed while the command is
	// incomplete). prompt is PS1 for the first row; prompt2 (PS2) prefixes each
	// continuation row. continueFn reports whether the current buffer needs more
	// input; when it does, Enter inserts a newline instead of submitting.
	prompt2    string
	continueFn func(string) bool

	// Multi-row redraw state: the rendered prompt+buffer block can span several
	// terminal rows (from wrapping and/or embedded newlines). prevCursorRow is
	// the cursor's row within that block at the last redraw, used to move back to
	// the top of the block before repainting. originCol is the column where the
	// prompt begins on the first row — non-zero when the previous command left
	// partial-line output — so the editor draws after it instead of erasing it.
	originCol     int
	prevCursorRow int
}

// viReadLine reads one complete command using the vi-mode editor. The command
// may span multiple lines: when Enter is pressed while continueFn reports the
// buffer is incomplete (e.g. a trailing backslash, an open compound command, or
// a pending heredoc), a newline is inserted and editing continues on a new row
// prefixed by prompt2 (PS2). All vi motions operate over the whole buffer, so
// the cursor can move across the embedded newlines.
// consoleRawMode and readKey are implemented per platform.
func viReadLine(prompt, prompt2 string, history []string, completer completeFn, continueFn func(string) bool) (string, error) {
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
		prompt:     prompt,
		prompt2:    prompt2,
		history:    append([]string(nil), history...),
		histIdx:    len(history),
		mode:       viInsert,
		completer:  completer,
		continueFn: continueFn,
		// Draw the prompt at the cursor's current column so partial-line output
		// from the previous command (e.g. printf with no trailing newline) is
		// preserved, matching bash.
		originCol: cursorColumn(),
	}
	vs.redraw()

	for {
		key, err := readKey()
		if err != nil {
			vs.endLine()
			return string(vs.buf), err
		}

		if vs.searching {
			vs.handleSearch(key)
			continue
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

// onEnter decides what pressing Return does: if the command is incomplete it
// inserts a newline at the cursor and keeps editing (returning done=false);
// otherwise it submits the whole buffer. This is what lets a multi-line command
// be built and freely navigated as a single buffer.
func (vs *viState) onEnter() (done bool, line string) {
	if vs.continueFn != nil && vs.continueFn(string(vs.buf)) {
		vs.saveUndo()
		vs.buf = append(vs.buf[:vs.pos], append([]rune{'\n'}, vs.buf[vs.pos:]...)...)
		vs.pos++
		vs.redraw()
		return false, ""
	}
	return true, string(vs.buf)
}

// ---- insert mode ----

func (vs *viState) handleInsert(key keyEvent) (done bool, line string, err error) {
	switch key.typ {
	case keyEnter:
		done, line = vs.onEnter()
		return done, line, nil
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
	case keyCtrlR:
		vs.startSearch(true, false) // emacs incremental reverse search
		return false, "", nil
	case keyCtrlS:
		vs.startSearch(false, false) // emacs incremental forward search
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
	vs.prevCursorRow = 0
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
		done, line = vs.onEnter()
		return done, line, nil
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
	case keyCtrlR:
		vs.startSearch(true, false) // emacs incremental reverse search
		return false, "", nil
	case keyCtrlS:
		vs.startSearch(false, false) // emacs incremental forward search
		return false, "", nil
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
	case '/':
		vs.startSearch(true, true) // vi search toward older history
	case '?':
		vs.startSearch(false, true) // vi search toward newer history
	case 'n':
		vs.repeatSearch(false)
	case 'N':
		vs.repeatSearch(true)
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

// ---- history search ----

// searchHistoryIn returns the index of the nearest history entry containing pat,
// scanning toward older entries (older=true, decreasing index) or newer entries
// (older=false, increasing index) starting just past from. Returns -1 if none.
func searchHistoryIn(history []string, pat string, older bool, from int) int {
	if pat == "" {
		return -1
	}
	if older {
		for i := from - 1; i >= 0 && i < len(history); i-- {
			if strings.Contains(history[i], pat) {
				return i
			}
		}
		return -1
	}
	for i := from + 1; i < len(history); i++ {
		if strings.Contains(history[i], pat) {
			return i
		}
	}
	return -1
}

// startSearch enters history search. vi selects the non-incremental vi style
// (/, ?); otherwise the incremental emacs style (Ctrl+R / Ctrl+S). older=true
// searches toward older commands, older=false toward newer.
func (vs *viState) startSearch(older, vi bool) {
	vs.searching = true
	vs.searchVi = vi
	vs.searchOlder = older
	vs.searchInput = nil
	vs.searchOrig = append([]rune(nil), vs.buf...)
	vs.searchPrevMode = vs.mode
	vs.redraw()
}

// endSearch leaves search mode, restoring the editor mode (vi searches drop into
// command mode, emacs searches return to the prior mode).
func (vs *viState) endSearch() {
	vs.searching = false
	if vs.searchVi {
		vs.mode = viNormal
	} else {
		vs.mode = vs.searchPrevMode
	}
	if vs.pos >= len(vs.buf) && vs.pos > 0 {
		vs.pos = len(vs.buf) - 1
	}
}

// cancelSearch restores the line as it was before the search began.
func (vs *viState) cancelSearch() {
	vs.buf = vs.searchOrig
	vs.pos = 0
	vs.histIdx = len(vs.history)
	vs.searching = false
	vs.mode = vs.searchPrevMode
	vs.redraw()
}

// applySearch (emacs incremental) previews the most recent match for the current
// pattern, searching from the newest entry each time the pattern changes.
func (vs *viState) applySearch() {
	if idx := searchHistoryIn(vs.history, string(vs.searchInput), vs.searchOlder, len(vs.history)); idx >= 0 {
		vs.histIdx = idx
		vs.buf = []rune(vs.history[idx])
		vs.pos = 0
	}
	vs.redraw()
}

// stepSearch (emacs Ctrl+R/Ctrl+S) moves to the next match in the given
// direction from the current position.
func (vs *viState) stepSearch(older bool) {
	vs.searchOlder = older
	if idx := searchHistoryIn(vs.history, string(vs.searchInput), older, vs.histIdx); idx >= 0 {
		vs.histIdx = idx
		vs.buf = []rune(vs.history[idx])
		vs.pos = 0
	}
	vs.redraw()
}

// doViSearch (vi / or ?) runs the search for the typed pattern on Enter, jumping
// to the match, then leaves search mode.
func (vs *viState) doViSearch() {
	pat := string(vs.searchInput)
	if pat != "" {
		vs.searchLast = pat
		if idx := searchHistoryIn(vs.history, pat, vs.searchOlder, vs.histIdx); idx >= 0 {
			vs.histIdx = idx
			vs.buf = []rune(vs.history[idx])
			vs.pos = 0
		}
	}
	vs.endSearch()
	vs.redraw()
}

// handleSearch processes a keystroke while in history-search mode.
func (vs *viState) handleSearch(key keyEvent) {
	if vs.searchVi {
		switch key.typ {
		case keyEnter:
			vs.doViSearch()
		case keyEscape, keyInterrupt:
			vs.cancelSearch()
		case keyBackspace:
			if len(vs.searchInput) > 0 {
				vs.searchInput = vs.searchInput[:len(vs.searchInput)-1]
			}
			vs.redraw()
		case keyRune:
			vs.searchInput = append(vs.searchInput, key.r)
			vs.redraw()
		}
		return
	}

	// emacs incremental
	switch key.typ {
	case keyEnter:
		vs.searchLast = string(vs.searchInput)
		vs.endSearch()
		vs.redraw()
	case keyEscape, keyInterrupt:
		vs.cancelSearch()
	case keyCtrlR:
		vs.stepSearch(true)
	case keyCtrlS:
		vs.stepSearch(false)
	case keyBackspace:
		if len(vs.searchInput) > 0 {
			vs.searchInput = vs.searchInput[:len(vs.searchInput)-1]
		}
		vs.applySearch()
	case keyRune:
		vs.searchInput = append(vs.searchInput, key.r)
		vs.applySearch()
	}
}

// repeatSearch advances to the next match in the same (reverse=false) or
// opposite (reverse=true) direction as the last search, for vi n / N.
func (vs *viState) repeatSearch(reverse bool) {
	if vs.searchLast == "" {
		return
	}
	older := vs.searchOlder
	if reverse {
		older = !older
	}
	if idx := searchHistoryIn(vs.history, vs.searchLast, older, vs.histIdx); idx >= 0 {
		vs.histIdx = idx
		vs.buf = []rune(vs.history[idx])
		vs.pos = 0
		if vs.pos >= len(vs.buf) && vs.pos > 0 {
			vs.pos = len(vs.buf) - 1
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

func (vs *viState) crlf() { fmt.Fprint(os.Stdout, "\r\n") }

// redraw repaints the prompt and buffer, handling a buffer that spans several
// terminal rows from wrapping and/or embedded newlines, then writes the result.
func (vs *viState) redraw() {
	cols := terminalCols()
	if cols < 1 {
		cols = 80
	}
	ps1, ps2, buf, pos := vs.prompt, vs.prompt2, vs.buf, vs.pos
	if vs.searching {
		if vs.searchVi {
			// vi style: the line becomes "/pattern" (or "?pattern"); the matched
			// command isn't shown until Enter jumps to it.
			sp := "/"
			if !vs.searchOlder {
				sp = "?"
			}
			ps1, ps2, buf, pos = sp, sp, vs.searchInput, len(vs.searchInput)
		} else {
			// emacs style: incremental prompt with the match previewed in buf.
			label := "reverse-i-search"
			if !vs.searchOlder {
				label = "i-search"
			}
			ps1 = "(" + label + ")`" + string(vs.searchInput) + "': "
			ps2 = ps1
		}
	}
	out, cursorRow := renderMultiline(ps1, ps2, buf, pos, cols, vs.originCol, vs.prevCursorRow)
	vs.prevCursorRow = cursorRow
	fmt.Fprint(os.Stdout, out)
}

// rowCol is a terminal row/column position within the rendered block (0-based).
type rowCol struct{ row, col int }

// vlLayout walks ps1 + buf (continuation rows prefixed by ps2) and reports the
// visual row/column of every buffer index 0..len(buf), modeling terminal
// autowrap and embedded newlines. A character that fills the last column leaves
// the column at cols ("pending wrap"); the next printable character then starts
// a new row, matching how terminals behave. endRow is the row of the final
// position. Pure, so the wrapping math can be tested directly.
func vlLayout(ps1, ps2 string, buf []rune, cols, originCol int) (positions []rowCol, endRow int) {
	if cols < 1 {
		cols = 80
	}
	positions = make([]rowCol, len(buf)+1)
	row := 0
	col := originCol + visibleLen(ps1) // first row begins where the prompt is drawn
	for col >= cols {                  // prompt past the right edge (or long prompt)
		row++
		col -= cols
	}
	for i := 0; i < len(buf); i++ {
		// A pending wrap from the previous character becomes a real new row when
		// the next printable character (not a newline) is placed.
		if col >= cols && buf[i] != '\n' {
			row++
			col = 0
		}
		positions[i] = rowCol{row, col}
		ch := buf[i]
		if ch == '\n' {
			row++
			col = visibleLen(ps2)
			for col >= cols {
				row++
				col -= cols
			}
			continue
		}
		w := runewidth.RuneWidth(ch)
		if w < 1 {
			w = 1
		}
		if col+w > cols {
			row++
			col = 0
		}
		col += w
	}
	positions[len(buf)] = rowCol{row, col}
	return positions, row
}

// renderMultiline produces the output that repaints ps1+buf (continuation rows
// prefixed by ps2) with the cursor at pos. The block begins at column originCol
// on its first row (non-zero when the previous command left partial-line output,
// which must be preserved). It moves up to the top of the previously rendered
// block (prevCursorRow rows), back to originCol, clears from there to the end of
// the screen, then rewrites the prompt and buffer relying on terminal autowrap
// for soft wraps and emitting CR+LF (plus ps2) for embedded newlines. Finally it
// moves the cursor to its target row and column. Returns the output and the
// cursor's row within the new block (to pass back as prevCursorRow next time).
func renderMultiline(ps1, ps2 string, buf []rune, pos, cols, originCol, prevCursorRow int) (out string, cursorRow int) {
	if cols < 1 {
		cols = 80
	}
	positions, endRow := vlLayout(ps1, ps2, buf, cols, originCol)
	cur := positions[pos]

	var sb strings.Builder

	// Move up to the block's first row, back to the origin column, then clear
	// from there down. Clearing from originCol (rather than column 0) preserves
	// any partial-line output to the left of the prompt.
	if prevCursorRow > 0 {
		fmt.Fprintf(&sb, "\x1b[%dA", prevCursorRow)
	}
	sb.WriteString("\r")
	if originCol > 0 {
		fmt.Fprintf(&sb, "\x1b[%dC", originCol)
	}
	sb.WriteString("\x1b[J")

	// Rewrite prompt + buffer. Embedded newlines become CR+LF followed by PS2;
	// soft wraps are left to the terminal.
	sb.WriteString(ps1)
	for i := 0; i < len(buf); i++ {
		if buf[i] == '\n' {
			sb.WriteString("\r\n")
			sb.WriteString(ps2)
			continue
		}
		sb.WriteRune(buf[i])
	}

	// After writing, the terminal cursor sits at the end position (endRow). Move
	// it up to the target row, then set the column.
	if up := endRow - cur.row; up > 0 {
		fmt.Fprintf(&sb, "\x1b[%dA", up)
	}
	sb.WriteString("\r")
	if cur.col > 0 {
		fmt.Fprintf(&sb, "\x1b[%dC", cur.col)
	}

	return sb.String(), cur.row
}
