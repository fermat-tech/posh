package main

import (
	"strings"
	"testing"
)

// TestRenderLineSingleRow: a short line fits on one row, so no vertical movement
// is emitted and the prompt+buffer appear verbatim.
func TestRenderLineSingleRow(t *testing.T) {
	out, maxRows, cursorDW := renderLine("$ ", []rune("echo hi"), 7, 80, 0, 0)
	if maxRows != 1 {
		t.Fatalf("maxRows = %d, want 1", maxRows)
	}
	if cursorDW != 7 {
		t.Fatalf("cursorDW = %d, want 7", cursorDW)
	}
	if !strings.Contains(out, "$ echo hi") {
		t.Fatalf("output missing prompt+buffer: %q", out)
	}
	// No up/down row movement for a single-row line.
	if strings.Contains(out, "\x1b[1A") || strings.Contains(out, "\x1b[1B") {
		t.Fatalf("unexpected vertical cursor movement: %q", out)
	}
}

// TestRenderLineWrapsToMultipleRows: a line longer than the terminal width must
// report the correct number of rows so the next refresh can clear them all.
func TestRenderLineWrapsToMultipleRows(t *testing.T) {
	cols := 10
	// prompt width 2 + 20 content = 22 cols -> ceil(22/10) = 3 rows.
	buf := []rune(strings.Repeat("x", 20))
	_, maxRows, _ := renderLine("$ ", buf, len(buf), cols, 0, 0)
	if maxRows != 3 {
		t.Fatalf("maxRows for wrapped line = %d, want 3", maxRows)
	}
}

// TestRenderLineClearsPreviousRows: when the previous render occupied several
// rows, the refresh moves down to the bottom and clears every row.
func TestRenderLineClearsPreviousRows(t *testing.T) {
	cols := 10
	// Previous render: 3 rows, cursor was on row 1 (prevCursorDW = 0).
	// New buffer is short (1 row). Expect: move down 2 rows, then clear rows.
	out, maxRows, _ := renderLine("$ ", []rune("hi"), 2, cols, 3, 0)
	if !strings.Contains(out, "\x1b[2B") {
		t.Fatalf("expected move-down-2 to reach last old row: %q", out)
	}
	// Two "clear row and go up" sequences for rows above the bottom.
	if got := strings.Count(out, "\r\x1b[0K\x1b[1A"); got != 2 {
		t.Fatalf("clear-and-up count = %d, want 2: %q", got, out)
	}
	// maxRows is sticky at the high-water mark.
	if maxRows != 3 {
		t.Fatalf("maxRows = %d, want 3 (high-water mark retained)", maxRows)
	}
}

// TestRenderLineCursorColumnMidBuffer: with the cursor in the middle of a
// single-row line, the column is set with CHA-relative movement.
func TestRenderLineCursorColumnMidBuffer(t *testing.T) {
	// prompt "$ " (2) + cursor after "echo" (4) => column 6.
	out, _, cursorDW := renderLine("$ ", []rune("echo hi"), 4, 80, 0, 0)
	if cursorDW != 4 {
		t.Fatalf("cursorDW = %d, want 4", cursorDW)
	}
	if !strings.Contains(out, "\r\x1b[6C") {
		t.Fatalf("expected cursor moved to column 6: %q", out)
	}
}

// TestRenderLineWideChars: wide (2-column) runes count as two display columns
// when computing wrap rows.
func TestRenderLineWideChars(t *testing.T) {
	cols := 10
	// 6 wide CJK chars = 12 display cols, + prompt 2 = 14 -> 2 rows.
	buf := []rune("漢字漢字漢字")
	_, maxRows, cursorDW := renderLine("$ ", buf, len(buf), cols, 0, 0)
	if maxRows != 2 {
		t.Fatalf("maxRows for wide chars = %d, want 2", maxRows)
	}
	if cursorDW != 12 {
		t.Fatalf("cursorDW for 6 wide chars = %d, want 12", cursorDW)
	}
}
