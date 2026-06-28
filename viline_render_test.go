package main

import (
	"strings"
	"testing"
)

// TestLayoutSingleRow: a short line stays on row 0; the cursor column tracks the
// prompt width plus the text before it.
func TestLayoutSingleRow(t *testing.T) {
	pos, endRow := vlLayout("$ ", "> ", []rune("echo hi"), 80, 0)
	if endRow != 0 {
		t.Fatalf("endRow = %d, want 0", endRow)
	}
	// cursor at end: prompt(2) + "echo hi"(7) = col 9 on row 0.
	if got := pos[7]; got.row != 0 || got.col != 9 {
		t.Fatalf("end position = %+v, want {0,9}", got)
	}
}

// TestLayoutSoftWrap: a line wider than the terminal wraps to a second row.
func TestLayoutSoftWrap(t *testing.T) {
	cols := 10
	buf := []rune(strings.Repeat("x", 20)) // 20 cols + prompt 2 = spans 3 rows
	pos, endRow := vlLayout("$ ", "> ", buf, cols, 0)
	if endRow != 2 {
		t.Fatalf("endRow = %d, want 2", endRow)
	}
	// First char is on row 0 col 2 (after the prompt).
	if got := pos[0]; got.row != 0 || got.col != 2 {
		t.Fatalf("pos[0] = %+v, want {0,2}", got)
	}
}

// TestLayoutEmbeddedNewline: an embedded newline starts a new row whose column
// begins after the PS2 prompt, regardless of wrapping.
func TestLayoutEmbeddedNewline(t *testing.T) {
	buf := []rune("ab\ncd")
	pos, endRow := vlLayout("$ ", "> ", buf, 80, 0)
	if endRow != 1 {
		t.Fatalf("endRow = %d, want 1 (two logical lines)", endRow)
	}
	// 'c' is index 3 (after a,b,\n); it sits on row 1 at col 2 (after "> ").
	if got := pos[3]; got.row != 1 || got.col != 2 {
		t.Fatalf("pos of 'c' = %+v, want {1,2}", got)
	}
	// End (after "cd") is row 1, col 4.
	if got := pos[len(buf)]; got.row != 1 || got.col != 4 {
		t.Fatalf("end = %+v, want {1,4}", got)
	}
}

// TestLayoutWideChars: wide runes occupy two columns when computing wraps.
func TestLayoutWideChars(t *testing.T) {
	cols := 10
	// prompt 2 + 6 wide chars (12 cols) = 14 -> spans 2 rows.
	buf := []rune("漢字漢字漢字")
	_, endRow := vlLayout("$ ", "> ", buf, cols, 0)
	if endRow != 1 {
		t.Fatalf("endRow for wide chars = %d, want 1", endRow)
	}
}

// TestRenderMultilineMovesToTopAndClears: a refresh after a multi-row render
// moves the cursor up to the block top and clears downward before repainting.
func TestRenderMultilineMovesToTopAndClears(t *testing.T) {
	// Previous render left the cursor on row 2 of the block.
	out, cursorRow := renderMultiline("$ ", "> ", []rune("a\nb\nc"), 5, 80, 0, 2)
	if !strings.Contains(out, "\x1b[2A") {
		t.Fatalf("expected move-up-2 to block top: %q", out)
	}
	if !strings.Contains(out, "\x1b[J") {
		t.Fatalf("expected clear-to-end-of-display: %q", out)
	}
	// Cursor ends on the last (3rd) logical line, row index 2.
	if cursorRow != 2 {
		t.Fatalf("cursorRow = %d, want 2", cursorRow)
	}
	// PS2 should prefix each continuation line in the output.
	if strings.Count(out, "> ") != 2 {
		t.Fatalf("expected 2 PS2 prefixes, got %d: %q", strings.Count(out, "> "), out)
	}
}

// TestRenderMultilineCursorOnFirstRow: with the cursor in the first line of a
// multi-line buffer, the refresh moves back up to that row.
func TestRenderMultilineCursorOnFirstRow(t *testing.T) {
	// buffer "ab\ncd", cursor at index 1 (within first line).
	out, cursorRow := renderMultiline("$ ", "> ", []rune("ab\ncd"), 1, 80, 0, 0)
	if cursorRow != 0 {
		t.Fatalf("cursorRow = %d, want 0", cursorRow)
	}
	// End is on row 1, so to put the cursor on row 0 it must move up once.
	if !strings.Contains(out, "\x1b[1A") {
		t.Fatalf("expected move-up-1 to reach first row: %q", out)
	}
}

// TestLayoutOriginColumn: when the prompt starts at a non-zero column (partial
// output to its left), the first row's columns are offset by originCol.
func TestLayoutOriginColumn(t *testing.T) {
	// originCol 5, prompt "$ " (2) -> first char of buffer at col 7.
	pos, _ := vlLayout("$ ", "> ", []rune("hi"), 80, 5)
	if got := pos[0]; got.row != 0 || got.col != 7 {
		t.Fatalf("pos[0] with originCol=5 = %+v, want {0,7}", got)
	}
}

// TestRenderMultilinePreservesPartialOutput: with a non-zero originCol, the
// refresh moves to that column and clears from there — it does NOT return to
// column 0 to clear, so partial-line output to the left survives.
func TestRenderMultilinePreservesPartialOutput(t *testing.T) {
	out, _ := renderMultiline("$ ", "> ", []rune("hi"), 2, 80, 5, 0)
	// Moves right to the origin column before clearing.
	if !strings.Contains(out, "\x1b[5C") {
		t.Fatalf("expected move to origin column 5: %q", out)
	}
	// Clears from the origin column, not the whole line: a bare CR followed
	// immediately by the screen-clear (without the forward move) would erase the
	// partial output, so ensure the forward move precedes the clear.
	idxFwd := strings.Index(out, "\x1b[5C")
	idxClr := strings.Index(out, "\x1b[J")
	if idxClr < idxFwd {
		t.Fatalf("clear must come after moving to origin column: %q", out)
	}
}
