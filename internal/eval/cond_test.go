package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCondFileTests(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	writeFile(t, file, "hi")

	cases := []struct {
		src  string
		want string
	}{
		{`[[ -f '` + file + `' ]] && echo yes`, "yes"},
		{`[[ -d '` + dir + `' ]] && echo yes`, "yes"},
		{`[[ -e '` + file + `' ]] && echo yes`, "yes"},
		{`[[ -e '` + file + `nope' ]] || echo yes`, "yes"},
		{`[[ -s '` + file + `' ]] && echo yes`, "yes"},
	}
	for _, c := range cases {
		if got := eval(t, c.src); got != c.want {
			t.Errorf("eval(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}

func TestCondStringTests(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{`x=""; [[ -z "$x" ]] && echo yes`, "yes"},
		{`x=a; [[ -n "$x" ]] && echo yes`, "yes"},
		{`a=foo; [[ $a == foo ]] && echo yes`, "yes"},
		{`a=foo; [[ $a != bar ]] && echo yes`, "yes"},
		{`a=abc; [[ $a < abd ]] && echo yes`, "yes"},
		{`a=abd; [[ $a > abc ]] && echo yes`, "yes"},
	}
	for _, c := range cases {
		if got := eval(t, c.src); got != c.want {
			t.Errorf("eval(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}

// TestCondPatternMatching covers ==/!= glob semantics: unquoted metachars are
// wildcards, quoted ones are literal, and quoting can be mixed within a
// single pattern word (foo"*"bar). filepath.Match alone cannot express the
// mixed case (its pattern syntax has no way to escape * next to a real
// wildcard), which is why [[ ]] uses its own sentinel-aware matcher.
func TestCondPatternMatching(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{`a=hello.txt; [[ $a == *.txt ]] && echo yes`, "yes"},
		{`a=hello.txt; [[ $a == "*.txt" ]] || echo yes`, "yes"},        // whole-word double-quoted: literal
		{`a=hello.txt; [[ $a == '*.txt' ]] || echo yes`, "yes"},       // whole-word single-quoted: literal
		{`a="foo*bar"; [[ $a == foo"*"bar ]] && echo yes`, "yes"},     // mixed: literal * matches literal *
		{`a="fooXbar"; [[ $a == foo"*"bar ]] || echo yes`, "yes"},     // mixed: literal * must not act as wildcard
		{`a=cat; [[ $a == [bc]at ]] && echo yes`, "yes"},              // character class
		{`a=dat; [[ $a == [bc]at ]] || echo yes`, "yes"},
		{`a=cat; [[ $a == [!xyz]at ]] && echo yes`, "yes"},            // negated character class
	}
	for _, c := range cases {
		if got := eval(t, c.src); got != c.want {
			t.Errorf("eval(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}

func TestCondArithmeticTests(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{`[[ 5 -gt 3 ]] && echo yes`, "yes"},
		{`[[ 5 -eq 5 ]] && echo yes`, "yes"},
		{`[[ 5 -ne 3 ]] && echo yes`, "yes"},
		{`[[ 3 -lt 5 ]] && echo yes`, "yes"},
		{`[[ 3 -le 3 ]] && echo yes`, "yes"},
		{`[[ 5 -ge 5 ]] && echo yes`, "yes"},
	}
	for _, c := range cases {
		if got := eval(t, c.src); got != c.want {
			t.Errorf("eval(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}

func TestCondLogicalCombinators(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{`[[ -n a && -n b ]] && echo yes`, "yes"},
		{`[[ -z a && -n b ]] || echo yes`, "yes"},
		{`[[ -z a || -n b ]] && echo yes`, "yes"},
		{`[[ ! -z a ]] && echo yes`, "yes"},
		{`[[ ( -z a || -n a ) && -n b ]] && echo yes`, "yes"},
		// ! binds to the single following term, not a whole && chain:
		// ! -z a && -n b  ==  (! -z a) && (-n b), not !( -z a && -n b ).
		{`[[ ! -z a && -n b ]] && echo yes`, "yes"},
	}
	for _, c := range cases {
		if got := eval(t, c.src); got != c.want {
			t.Errorf("eval(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}

// TestCondShortCircuit ensures && and || actually short-circuit (the right
// side's side effect must not run when the left side already decides it).
func TestCondShortCircuit(t *testing.T) {
	if got := eval(t, `f() { echo ran; return 1; }; false && f; echo done`); got != "done" {
		t.Fatalf("&& should short-circuit, got %q", got)
	}
	if got := eval(t, `f() { echo ran; return 1; }; true || f; echo done`); got != "done" {
		t.Fatalf("|| should short-circuit, got %q", got)
	}
}

// TestCondRegex covers =~ and $POSH_REMATCH (posh's name for bash's
// $BASH_REMATCH).
func TestCondRegex(t *testing.T) {
	if got := eval(t, `[[ "hello123" =~ ^[a-z]+[0-9]+$ ]] && echo yes`); got != "yes" {
		t.Fatalf("basic regex match = %q", got)
	}
	if got := eval(t, `[[ "hello" =~ ^[0-9]+$ ]] || echo yes`); got != "yes" {
		t.Fatalf("regex non-match = %q", got)
	}
	// Capture groups populate POSH_REMATCH: [0] is the whole match, [1..] are
	// the parenthesized groups. Tried both quoted and unquoted -- bash accepts
	// an unquoted regex containing ( ) / | for the operand immediately after
	// =~ (verified against a real bash 5.2), which posh's lexer initially did
	// not (see TestTokenizeRegexOperandParens for the fix).
	for _, pattern := range []string{
		`^([a-z]+)([0-9]+)$`,
		`"^([a-z]+)([0-9]+)$"`,
	} {
		src := `[[ "hello123" =~ ` + pattern + ` ]] && echo "${POSH_REMATCH[0]}|${POSH_REMATCH[1]}|${POSH_REMATCH[2]}"`
		if got := eval(t, src); got != "hello123|hello|123" {
			t.Fatalf("POSH_REMATCH for pattern %s = %q, want %q", pattern, got, "hello123|hello|123")
		}
	}
	// Alternation (|) is likewise relaxed unquoted for the =~ operand.
	if got := eval(t, `a=cat; [[ $a =~ ^(cat|dog)$ ]] && echo yes`); got != "yes" {
		t.Fatalf("unquoted alternation in =~ = %q", got)
	}
	// Only the word right after =~ is relaxed: && afterward must still work
	// as the list-level operator, not get swallowed into the regex.
	if got := eval(t, `a=cat; [[ $a =~ ^(cat|dog)$ && 1 -eq 1 ]] && echo yes`); got != "yes" {
		t.Fatalf("&& after an unquoted regex operand = %q", got)
	}
	// A failed match must clear any stale POSH_REMATCH from an earlier one.
	got := eval(t, `[[ "hello123" =~ ^([a-z]+)([0-9]+)$ ]]; [[ "nomatch" =~ ^([0-9]+)$ ]]; echo "[${POSH_REMATCH[0]}]"`)
	if got != "[]" {
		t.Fatalf("POSH_REMATCH after a failed match = %q, want cleared", got)
	}
}

// TestCondRegexInvalidPattern covers the exit-2 error path: a syntactically
// invalid regex is a hard error, not just a false result.
func TestCondRegexInvalidPattern(t *testing.T) {
	sh := New("posh")
	code := sh.EvalString(`[[ "x" =~ "(" ]]`)
	if code != 2 {
		t.Fatalf("invalid regex exit code = %d, want 2", code)
	}
}

func TestCondDashV(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{`myvar=hello; [[ -v myvar ]] && echo yes`, "yes"},
		{`[[ -v totally_unset_xyz ]] || echo yes`, "yes"},
		{`[[ -v RANDOM ]] && echo yes`, "yes"},   // dynamic vars count as set
		{`[[ -v SECONDS ]] && echo yes`, "yes"},
		{`[[ -v POSHPID ]] && echo yes`, "yes"},
		{`arr=(a b c); [[ -v arr[1] ]] && echo yes`, "yes"},
		{`arr=(a b c); [[ -v arr[9] ]] || echo yes`, "yes"},
	}
	for _, c := range cases {
		if got := eval(t, c.src); got != c.want {
			t.Errorf("eval(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}

func TestCondDashO(t *testing.T) {
	if got := eval(t, `set -o xtrace; [[ -o xtrace ]] && echo yes`); got != "yes" {
		t.Fatalf("-o set = %q", got)
	}
	if got := eval(t, `set +o xtrace; [[ -o xtrace ]] || echo yes`); got != "yes" {
		t.Fatalf("-o unset = %q", got)
	}
}

func TestCondFileComparisons(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	writeFile(t, a, "a")
	writeFile(t, b, "b")

	if got := eval(t, `[[ '`+a+`' -ef '`+a+`' ]] && echo yes`); got != "yes" {
		t.Fatalf("-ef same file = %q", got)
	}
	if got := eval(t, `[[ '`+a+`' -ef '`+b+`' ]] || echo yes`); got != "yes" {
		t.Fatalf("-ef distinct files = %q", got)
	}
}

// TestCondNeedsContinuation ensures an unterminated [[ waits for more input
// at the interactive prompt (PS2), rather than being reported as a syntax
// error on the first Enter press -- the same class of fix already applied to
// unterminated quotes and array literals.
func TestCondNeedsContinuation(t *testing.T) {
	sh := New("posh")
	var buf strings.Builder
	sh.Stdout = &buf
	code := sh.EvalString("[[ -f go.mod\n&& -d internal ]]\necho after")
	_ = code
	if !strings.Contains(buf.String(), "after") {
		t.Fatalf("multi-line [[ ]] did not evaluate correctly, output = %q", buf.String())
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writeFile(%q): %v", path, err)
	}
}
