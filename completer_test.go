package main

import (
	"sort"
	"testing"

	"github.com/fermat-tech/posh/internal/eval"
)

// TestVarCandidatesIncludesArrayVars reproduces the reported case: bash's TAB
// completion for "$BASH_VERSI" lists both $BASH_VERSINFO (an array) and
// $BASH_VERSION (a scalar). varCandidates previously only looked at scalar
// variables (Shell.Vars()), so an array-only match like POSH_VERSINFO was
// invisible to completion — a prefix matching both a scalar and an array
// variable resolved as a single match instead of offering both alternatives.
func TestVarCandidatesIncludesArrayVars(t *testing.T) {
	sh := eval.New("posh")
	sh.SetVersion("v1.3.51") // sets $POSH_VERSION (scalar) and $POSH_VERSINFO (array)
	c := &poshCompleter{sh: sh}

	got := c.varCandidates("$POSH_VERSI")
	sort.Strings(got)
	want := []string{"$POSH_VERSINFO", "$POSH_VERSION"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("varCandidates(%q) = %v, want %v", "$POSH_VERSI", got, want)
	}
}

func TestLooksLikePath(t *testing.T) {
	yes := []string{"~/sle", "./script", "../x", "/usr/bin", `dir\file`, "a/b"}
	for _, s := range yes {
		if !looksLikePath(s) {
			t.Errorf("looksLikePath(%q) = false, want true", s)
		}
	}
	no := []string{"ls", "echo", "git", "posh", ""}
	for _, s := range no {
		if looksLikePath(s) {
			t.Errorf("looksLikePath(%q) = true, want false", s)
		}
	}
}

func TestFindWordStart(t *testing.T) {
	cases := []struct {
		head      string
		wantStart int
		wantQuote rune
	}{
		{"ls ~/sl", 3, 0},        // last word starts after the space
		{"echo", 0, 0},           // single word starts at 0
		{"a | b", 4, 0},          // after a pipe
		{"cmd 'open quote", 4, '\''}, // unclosed single quote
		{`cmd "open`, 4, '"'},    // unclosed double quote
		{`echo "closed" x`, 14, 0}, // word after a closed quoted string
	}
	for _, c := range cases {
		start, q := findWordStart(c.head)
		if start != c.wantStart || q != c.wantQuote {
			t.Errorf("findWordStart(%q) = (%d, %q), want (%d, %q)",
				c.head, start, q, c.wantStart, c.wantQuote)
		}
	}
}
