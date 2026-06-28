package main

import "testing"

// emacsState returns a viState in emacs mode with the given buffer and cursor at
// the end.
func emacsState(line string) *viState {
	return &viState{
		buf:   []rune(line),
		pos:   len([]rune(line)),
		mode:  viInsert,
		emacs: true,
	}
}

func feed(t *testing.T, vs *viState, keys ...keyEvent) {
	t.Helper()
	withSilencedStdout(t, func() {
		for _, k := range keys {
			vs.handleInsert(k)
		}
	})
}

func ck(k keyType) keyEvent { return keyEvent{typ: k} }
func rk(r rune) keyEvent    { return keyEvent{typ: keyRune, r: r} }

func TestEmacsMovementAndInsert(t *testing.T) {
	vs := emacsState("abc")
	// Ctrl+B twice -> cursor between 'a' and 'b'; insert 'X'.
	feed(t, vs, ck(keyCtrlB), ck(keyCtrlB), rk('X'))
	if got := string(vs.buf); got != "aXbc" {
		t.Fatalf("Ctrl+B then insert = %q, want %q", got, "aXbc")
	}
	// Cursor is after 'X' (pos 2); one Ctrl+F moves past 'b'; insert 'Y'.
	feed(t, vs, ck(keyCtrlF), rk('Y'))
	if got := string(vs.buf); got != "aXbYc" {
		t.Fatalf("Ctrl+F then insert = %q, want %q", got, "aXbYc")
	}
}

func TestEmacsKillAndYank(t *testing.T) {
	vs := emacsState("hello world")
	// Ctrl+U kills to start (here, the whole line since cursor is at end);
	// the killed text goes to the yank buffer.
	feed(t, vs, ck(keyCtrlU))
	if len(vs.buf) != 0 {
		t.Fatalf("Ctrl+U should clear the line, got %q", string(vs.buf))
	}
	// Ctrl+Y pastes it back.
	feed(t, vs, ck(keyCtrlY))
	if got := string(vs.buf); got != "hello world" {
		t.Fatalf("Ctrl+Y = %q, want %q", got, "hello world")
	}
}

func TestEmacsHistoryCtrlPN(t *testing.T) {
	vs := &viState{
		history: []string{"first", "second"},
		histIdx: 2,
		mode:    viInsert,
		emacs:   true,
	}
	feed(t, vs, ck(keyCtrlP)) // -> "second"
	if got := string(vs.buf); got != "second" {
		t.Fatalf("Ctrl+P = %q, want %q", got, "second")
	}
	feed(t, vs, ck(keyCtrlP)) // -> "first"
	if got := string(vs.buf); got != "first" {
		t.Fatalf("Ctrl+P again = %q, want %q", got, "first")
	}
	feed(t, vs, ck(keyCtrlN)) // -> "second"
	if got := string(vs.buf); got != "second" {
		t.Fatalf("Ctrl+N = %q, want %q", got, "second")
	}
}

func TestEmacsEscIsInert(t *testing.T) {
	vs := emacsState("abc")
	feed(t, vs, ck(keyEscape), rk('d'))
	// Esc does nothing in emacs mode; 'd' is inserted normally.
	if vs.mode != viInsert {
		t.Fatalf("Esc should not switch modes in emacs mode")
	}
	if got := string(vs.buf); got != "abcd" {
		t.Fatalf("after Esc then 'd' = %q, want %q", got, "abcd")
	}
}
