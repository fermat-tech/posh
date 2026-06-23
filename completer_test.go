package main

import "testing"

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
