package eval

import (
	"bytes"
	"strings"
	"testing"
)

// eval runs src in a fresh shell and returns its stdout with the trailing
// newline removed. All carriage returns are stripped so results don't depend on
// whether an external tool such as cat emits Windows (CRLF) or Unix (LF) line
// endings — this normalizes trailing, internal (multi-line heredoc), and any
// stray lone-CR output uniformly. It fails the test if a top-level exit escapes
// (use run from exit_test.go for those cases).
func eval(t *testing.T, src string) string {
	t.Helper()
	sh := New("posh")
	var buf bytes.Buffer
	sh.Stdout = &buf
	sh.Stderr = &buf
	sh.EvalString(src)
	out := strings.ReplaceAll(buf.String(), "\r", "")
	return strings.TrimRight(out, "\n")
}

// evalRaw runs src in the given shell and returns stdout verbatim (no trimming),
// for tests that care about exact whitespace such as `echo -n`.
func evalRaw(sh *Shell, src string) string {
	var buf bytes.Buffer
	sh.Stdout = &buf
	sh.Stderr = &buf
	sh.EvalString(src)
	return buf.String()
}

func TestVarAssignmentAndExpansion(t *testing.T) {
	if got := eval(t, `NAME=world; echo "Hello $NAME"`); got != "Hello world" {
		t.Fatalf("got %q", got)
	}
	if got := eval(t, `NAME=world; echo "Hello ${NAME}!"`); got != "Hello world!" {
		t.Fatalf("got %q", got)
	}
}

func TestSingleVsDoubleQuotes(t *testing.T) {
	if got := eval(t, `X=v; echo '$X'`); got != "$X" {
		t.Fatalf("single quotes should not expand, got %q", got)
	}
	if got := eval(t, `X=v; echo "$X"`); got != "v" {
		t.Fatalf("double quotes should expand, got %q", got)
	}
}

func TestCommandSubstitution(t *testing.T) {
	if got := eval(t, `echo "[$(echo inner)]"`); got != "[inner]" {
		t.Fatalf("got %q", got)
	}
}

func TestCommandSubstitutionStripsCR(t *testing.T) {
	// Windows tools emit CRLF; command substitution must not leave stray \r on
	// the captured value or on the words it splits into.
	if got := eval(t, `x=$(printf 'hi\r\n'); printf '[%s]' "$x"`); got != "[hi]" {
		t.Fatalf("scalar capture = %q, want [hi]", got)
	}
	got := eval(t, `for n in $(printf '1\r\n2\r\n3\r\n'); do printf '[%s]' "$n"; done`)
	if got != "[1][2][3]" {
		t.Fatalf("split capture = %q, want [1][2][3]", got)
	}
}

func TestArithmeticExpansion(t *testing.T) {
	cases := map[string]string{
		`echo $((2 + 3 * 4))`:   "14",
		`echo $(((2 + 3) * 4))`: "20",
		`echo $((10 / 3))`:      "3",
		`echo $((10 % 3))`:      "1",
		`echo $((2 > 1))`:       "1",
		`echo $((1 == 2))`:      "0",
		`echo $((7 - 9))`:       "-2",
		`echo $((1 << 4))`:      "16",
		`echo $((256 >> 2))`:    "64",
		`echo $((1 << 3 == 8))`: "1",
	}
	for src, want := range cases {
		if got := eval(t, src); got != want {
			t.Errorf("%s = %q, want %q", src, got, want)
		}
	}
}

func TestArithDirect(t *testing.T) {
	sh := New("posh")
	sh.setVar("x", "5")
	if got := evalArith(sh, "x * 2 + 1"); got != 11 {
		t.Fatalf("evalArith x*2+1 with x=5 = %d, want 11", got)
	}
}

func TestSpecialParams(t *testing.T) {
	if got := eval(t, `true; echo $?`); got != "0" {
		t.Fatalf("$? after true = %q", got)
	}
	if got := eval(t, `false; echo $?`); got != "1" {
		t.Fatalf("$? after false = %q", got)
	}
}

func TestBraceExpansion(t *testing.T) {
	if got := eval(t, `echo a{1,2,3}b`); got != "a1b a2b a3b" {
		t.Fatalf("list brace: got %q", got)
	}
	if got := eval(t, `echo {1..4}`); got != "1 2 3 4" {
		t.Fatalf("range brace: got %q", got)
	}
}

func TestBraceExpandUnit(t *testing.T) {
	got := braceExpand("x{a,b}y")
	if len(got) != 2 || got[0] != "xay" || got[1] != "xby" {
		t.Fatalf("braceExpand = %v", got)
	}
	// No braces: identity.
	if got := braceExpand("plain"); len(got) != 1 || got[0] != "plain" {
		t.Fatalf("braceExpand identity = %v", got)
	}
}

func TestRangeExpandUnit(t *testing.T) {
	got := tryRangeExpand("1..3")
	if len(got) != 3 || got[0] != "1" || got[2] != "3" {
		t.Fatalf("tryRangeExpand(1..3) = %v", got)
	}
	if tryRangeExpand("notarange") != nil {
		t.Fatalf("non-range should return nil")
	}
}

func TestWordSplit(t *testing.T) {
	sh := New("posh")
	got := sh.wordSplit("a  b\tc")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("wordSplit = %v", got)
	}
	if got := sh.wordSplit("single"); len(got) != 1 {
		t.Fatalf("wordSplit single = %v", got)
	}
}

func TestUnprotectWordIdentity(t *testing.T) {
	if got := unprotectWord("plain text"); got != "plain text" {
		t.Fatalf("unprotectWord changed plain text: %q", got)
	}
}

func TestUnquotedWordSplittingInCommand(t *testing.T) {
	// Unquoted expansion is word-split into separate arguments; quoted is not.
	// A function's $# counts the arguments it actually received.
	if got := eval(t, `f() { echo $#; }; X="a b c"; f $X`); got != "3" {
		t.Fatalf("unquoted split count = %q, want 3", got)
	}
	if got := eval(t, `f() { echo $#; }; X="a b c"; f "$X"`); got != "1" {
		t.Fatalf("quoted split count = %q, want 1", got)
	}
}

func TestTildeExpansion(t *testing.T) {
	sh := New("posh")
	sh.setVar("HOME", "/home/test")
	if got := sh.ExpandWord("~"); got != "/home/test" {
		t.Fatalf("~ = %q", got)
	}
	got := sh.ExpandWord("~/sub")
	if !strings.Contains(got, "sub") || strings.HasPrefix(got, "~") {
		t.Fatalf("~/sub = %q", got)
	}
}

func TestANSICQuoting(t *testing.T) {
	// $'\t' is a literal tab.
	if got := eval(t, `printf '%s' $'a\tb'`); got != "a\tb" {
		t.Fatalf("ANSI-C tab = %q", got)
	}
}
