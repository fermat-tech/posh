package eval

import "testing"

func TestHeredocExpanding(t *testing.T) {
	src := "NAME=posh\ncat << EOF\nhello $NAME\nEOF"
	if got := eval(t, src); got != "hello posh" {
		t.Fatalf("expanding heredoc = %q", got)
	}
}

func TestHeredocQuotedDelimiterIsLiteral(t *testing.T) {
	src := "NAME=posh\ncat << 'EOF'\nhello $NAME\nEOF"
	if got := eval(t, src); got != "hello $NAME" {
		t.Fatalf("literal heredoc = %q", got)
	}
}

func TestHeredocStripTabs(t *testing.T) {
	// <<- strips leading tabs from body and the closing delimiter line.
	src := "cat <<- EOF\n\t\tindented\n\tEOF"
	if got := eval(t, src); got != "indented" {
		t.Fatalf("tab-stripped heredoc = %q", got)
	}
}

func TestHereString(t *testing.T) {
	if got := eval(t, `cat <<< "hello world"`); got != "hello world" {
		t.Fatalf("here-string = %q", got)
	}
	if got := eval(t, `NAME=posh; cat <<< "hi $NAME"`); got != "hi posh" {
		t.Fatalf("here-string expansion = %q", got)
	}
}

func TestExpandHeredocBodyUnit(t *testing.T) {
	sh := New("posh")
	sh.setVar("X", "val")
	if got := sh.expandHeredocBody("a $X b\n"); got != "a val b\n" {
		t.Fatalf("expandHeredocBody = %q", got)
	}
	// Backslash-dollar suppresses expansion.
	if got := sh.expandHeredocBody(`price \$5`); got != "price $5" {
		t.Fatalf("escaped dollar = %q", got)
	}
}
