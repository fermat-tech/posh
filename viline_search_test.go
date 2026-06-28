package main

import "testing"

func TestSearchHistoryIn(t *testing.T) {
	hist := []string{
		"echo one",     // 0
		"ls -l",        // 1
		"echo two",     // 2
		"grep foo bar", // 3
		"echo three",   // 4
	}
	n := len(hist)

	// Older search (vi /, Ctrl+R): start past the end, find most recent match.
	if got := searchHistoryIn(hist, "echo", true, n); got != 4 {
		t.Fatalf("older 'echo' from end = %d, want 4", got)
	}
	// Continue older from index 4 -> should find index 2.
	if got := searchHistoryIn(hist, "echo", true, 4); got != 2 {
		t.Fatalf("older 'echo' from 4 = %d, want 2", got)
	}
	// Continue older from index 2 -> index 0.
	if got := searchHistoryIn(hist, "echo", true, 2); got != 0 {
		t.Fatalf("older 'echo' from 2 = %d, want 0", got)
	}
	// No earlier match.
	if got := searchHistoryIn(hist, "echo", true, 0); got != -1 {
		t.Fatalf("older 'echo' from 0 = %d, want -1", got)
	}

	// Newer search (vi ?): from index 0 forward finds the next match.
	if got := searchHistoryIn(hist, "echo", false, 0); got != 2 {
		t.Fatalf("newer 'echo' from 0 = %d, want 2", got)
	}

	// Substring anywhere in the entry.
	if got := searchHistoryIn(hist, "foo", true, n); got != 3 {
		t.Fatalf("older 'foo' = %d, want 3", got)
	}
	// No match at all.
	if got := searchHistoryIn(hist, "zzz", true, n); got != -1 {
		t.Fatalf("missing pattern = %d, want -1", got)
	}
	// Empty pattern never matches.
	if got := searchHistoryIn(hist, "", true, n); got != -1 {
		t.Fatalf("empty pattern = %d, want -1", got)
	}
}

// TestSearchInteraction drives a viState through a search the way the key loop
// would, verifying the matched command lands in the buffer.
func TestSearchInteraction(t *testing.T) {
	vs := &viState{
		history: []string{"echo one", "ls -l", "echo two", "grep foo"},
		histIdx: 4,
		mode:    viNormal,
	}
	// Avoid terminal I/O from redraw during the test.
	withSilencedStdout(t, func() {
		vs.startSearch(true)             // "/"
		vs.handleSearch(runeKey('f'))    // type "f"
		vs.handleSearch(runeKey('o'))    // "fo"
		vs.handleSearch(keyEvent{typ: keyEnter})
	})
	if got := string(vs.buf); got != "grep foo" {
		t.Fatalf("after search for 'fo' buffer = %q, want %q", got, "grep foo")
	}
	if vs.searching {
		t.Fatalf("search should have ended on Enter")
	}
}

func runeKey(r rune) keyEvent { return keyEvent{typ: keyRune, r: r} }
