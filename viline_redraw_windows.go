//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"
)

// redraw rewrites the current input line in place.
// Uses GetConsoleScreenBufferInfo to measure actual cursor columns instead of
// computing display widths, so it is correct regardless of how the terminal
// renders wide characters (emoji, CJK) — which varies between Windows Terminal,
// conhost, etc.
func (vs *viState) redraw() {
	setCursorX(vs.originCol)

	// Write prompt + the portion of the buffer before the cursor position.
	// Then query the actual cursor column — this is our exact target, no width
	// arithmetic needed.
	fmt.Fprint(os.Stdout, vs.prompt+string(vs.buf[:vs.pos]))
	targetCol := cursorColumn()

	// Write the rest of the buffer.
	fmt.Fprint(os.Stdout, string(vs.buf[vs.pos:]))
	endCol := cursorColumn()

	// Blank out stale characters from a previous longer line.
	if erase := vs.lastDisplayLen - endCol; erase > 0 {
		fmt.Fprint(os.Stdout, strings.Repeat(" ", erase))
	}
	vs.lastDisplayLen = endCol

	// Move cursor to the target position.
	setCursorX(targetCol)
}
