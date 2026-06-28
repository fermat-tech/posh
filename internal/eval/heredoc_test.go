package eval

import "testing"

func TestHeredocExpanding(t *testing.T) {
	src := "NAME=posh\ncat << EOF\nhello $NAME\nEOF"
	if got := eval(t, src); got != "hello posh" {
		t.Fatalf("expanding heredoc = %q", got)
	}
}

func TestHeredocIntoPipeline(t *testing.T) {
	// The heredoc feeds the first stage; its output must reach the next command.
	src := "cat << EOF | cat\nThis is a *\ntest\nbye\nEOF"
	if got := eval(t, src); got != "This is a *\ntest\nbye" {
		t.Fatalf("heredoc | cat = %q", got)
	}
	// Heredoc piped into a counting command.
	if got := eval(t, "cat << EOF | wc -l\na\nb\nc\nEOF"); got != "3" {
		t.Fatalf("heredoc | wc -l = %q", got)
	}
}

func TestHeredocInCommandSubstitution(t *testing.T) {
	// A heredoc feeding a pipeline inside $(...), with the closing ) on its own
	// line after the delimiter (the reported case). Use <> markers so the
	// captured output isn't glob-expanded by the outer echo.
	src := "echo $(cat << 'HD' | while read l; do echo \"<$l>\"; done\na\nb\nc\nHD\n)"
	if got := eval(t, src); got != "<a> <b> <c>" {
		t.Fatalf("heredoc in cmdsub = %q", got)
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
