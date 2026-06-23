//go:build !windows

package main

import (
	"fmt"
	"os"
	"strings"
)

// redraw rewrites the current input line in place.
// Uses runewidth-based column counting (correct for Unix terminals where
// emoji and CJK characters occupy 2 columns).
func (vs *viState) redraw() {
	promptVW := visibleLen(vs.prompt)
	contentW := bufDisplayWidth(vs.buf)
	currentLen := promptVW + contentW

	setCursorX(vs.originCol)

	var sb strings.Builder
	sb.WriteString(vs.prompt)
	sb.WriteString(string(vs.buf))

	// Erase leftover characters if the line got shorter since last redraw.
	endCol := currentLen
	if vs.lastDisplayLen > endCol {
		endCol = vs.lastDisplayLen
	}
	for i := currentLen; i < vs.lastDisplayLen; i++ {
		sb.WriteByte(' ')
	}
	vs.lastDisplayLen = currentLen

	// Move cursor back to the correct position using backspaces.
	target := promptVW + bufDisplayWidth(vs.buf[:vs.pos])
	for i := target; i < endCol; i++ {
		sb.WriteByte('\b')
	}

	fmt.Fprint(os.Stdout, sb.String())
}
