package main

import (
	"os"
	"strings"
	"testing"
)

// newViState builds a minimal viState with the given buffer, cursor at end, and
// a completer, suitable for exercising doComplete.
func newViState(line string, completer completeFn) *viState {
	return &viState{
		buf:       []rune(line),
		pos:       len([]rune(line)),
		mode:      viInsert,
		completer: completer,
	}
}

// doComplete writes terminal output via redraw; silence it so test logs stay
// clean. Cursor queries fail harmlessly without a real console.
func withSilencedStdout(t *testing.T, fn func()) {
	t.Helper()
	old := os.Stdout
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		fn()
		return
	}
	os.Stdout = devnull
	defer func() {
		os.Stdout = old
		devnull.Close()
	}()
	fn()
}

// TestDoCompleteExtendingWord reproduces the panic from completing a word whose
// completion is longer than what was typed (e.g. "~/sleep2"): doComplete used a
// stale cursor index to slice the original line and went out of range.
func TestDoCompleteExtendingWord(t *testing.T) {
	line := `cmd ~/sl`
	completer := func(l string, pos int) (string, []string, string) {
		// head = everything before the word; completions extend "~/sl".
		return "cmd ", []string{"~/sleep2/", "~/sleepy/"}, ""
	}
	vs := newViState(line, completer)

	withSilencedStdout(t, func() {
		vs.doComplete() // must not panic
	})

	// The common prefix "~/sleep" should have been applied to the buffer.
	if got := string(vs.buf); !strings.HasPrefix(got, "cmd ~/sleep") {
		t.Fatalf("buffer after completion = %q, want prefix %q", got, "cmd ~/sleep")
	}
	if vs.pos != len(vs.buf) {
		t.Fatalf("cursor = %d, want end of buffer %d", vs.pos, len(vs.buf))
	}
}

// TestDoCompleteSingleMatch covers the single-completion branch.
func TestDoCompleteSingleMatch(t *testing.T) {
	completer := func(l string, pos int) (string, []string, string) {
		return "cat ", []string{"notes.txt"}, ""
	}
	vs := newViState("cat no", completer)
	withSilencedStdout(t, func() { vs.doComplete() })

	if got := string(vs.buf); got != "cat notes.txt " {
		t.Fatalf("single completion = %q, want %q", got, "cat notes.txt ")
	}
}

// TestDoCompleteDirectoryNoTrailingSpace verifies a directory completion keeps
// the trailing slash and does not append a space.
func TestDoCompleteDirectoryNoTrailingSpace(t *testing.T) {
	completer := func(l string, pos int) (string, []string, string) {
		return "cd ", []string{"projects/"}, ""
	}
	vs := newViState("cd pro", completer)
	withSilencedStdout(t, func() { vs.doComplete() })

	if got := string(vs.buf); got != "cd projects/" {
		t.Fatalf("directory completion = %q, want %q", got, "cd projects/")
	}
}

// TestDoCompleteMidLinePreservesTail covers completing a word in the middle of
// the line (e.g. after a vi `cw`): the text after the cursor must be preserved,
// not erased, and the cursor must land just after the inserted word.
func TestDoCompleteMidLinePreservesTail(t *testing.T) {
	// Line "cat no file2", cursor right after "no" (pos 6); tail is " file2".
	completer := func(l string, pos int) (string, []string, string) {
		return "cat ", []string{"notes.txt"}, " file2"
	}
	vs := &viState{buf: []rune("cat no file2"), pos: 6, mode: viInsert, completer: completer}
	withSilencedStdout(t, func() { vs.doComplete() })

	if got := string(vs.buf); got != "cat notes.txt file2" {
		t.Fatalf("mid-line completion = %q, want %q", got, "cat notes.txt file2")
	}
	// The tail already begins with a space, so no separator is added and the
	// cursor sits right after "cat notes.txt" (len 13), before the existing space.
	if vs.pos != len([]rune("cat notes.txt")) {
		t.Fatalf("cursor = %d, want %d", vs.pos, len([]rune("cat notes.txt")))
	}
}

// TestDoCompleteMidLineMultiPreservesTail covers the common-prefix branch with a
// non-empty tail.
func TestDoCompleteMidLineMultiPreservesTail(t *testing.T) {
	completer := func(l string, pos int) (string, []string, string) {
		return "cmd ", []string{"report1", "report2"}, " arg"
	}
	vs := &viState{buf: []rune("cmd rep arg"), pos: 7, mode: viInsert, completer: completer}
	withSilencedStdout(t, func() { vs.doComplete() })

	if got := string(vs.buf); got != "cmd report arg" {
		t.Fatalf("mid-line multi completion = %q, want %q", got, "cmd report arg")
	}
	if vs.pos != len([]rune("cmd report")) {
		t.Fatalf("cursor = %d, want %d", vs.pos, len([]rune("cmd report")))
	}
}

// TestDoCompleteNoMatchesIsNoop ensures an empty completion list leaves the
// buffer untouched and does not panic.
func TestDoCompleteNoMatchesIsNoop(t *testing.T) {
	completer := func(l string, pos int) (string, []string, string) {
		return "cmd ", nil, ""
	}
	vs := newViState("cmd zzz", completer)
	withSilencedStdout(t, func() { vs.doComplete() })

	if got := string(vs.buf); got != "cmd zzz" {
		t.Fatalf("buffer changed on no-match: %q", got)
	}
}
