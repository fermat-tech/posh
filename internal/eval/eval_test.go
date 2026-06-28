package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPipeline(t *testing.T) {
	if got := eval(t, `printf 'a\nb\nc\n' | grep b`); got != "b" {
		t.Fatalf("pipeline = %q", got)
	}
}

func TestListOperators(t *testing.T) {
	if got := eval(t, `true && echo yes`); got != "yes" {
		t.Fatalf("&& = %q", got)
	}
	if got := eval(t, `false || echo recovered`); got != "recovered" {
		t.Fatalf("|| = %q", got)
	}
	if got := eval(t, `false && echo nope`); got != "" {
		t.Fatalf("&& short-circuit = %q", got)
	}
	if got := eval(t, `echo a; echo b`); got != "a\nb" {
		t.Fatalf("semicolon sequence = %q", got)
	}
}

func TestPipelineNegation(t *testing.T) {
	if got := eval(t, `! false && echo yes`); got != "yes" {
		t.Fatalf("! false = %q", got)
	}
}

func TestIfElse(t *testing.T) {
	if got := eval(t, `if true; then echo T; else echo F; fi`); got != "T" {
		t.Fatalf("if-true = %q", got)
	}
	if got := eval(t, `if false; then echo T; else echo F; fi`); got != "F" {
		t.Fatalf("if-false = %q", got)
	}
	if got := eval(t, `if false; then echo A; elif true; then echo B; else echo C; fi`); got != "B" {
		t.Fatalf("elif = %q", got)
	}
}

func TestForLoop(t *testing.T) {
	if got := eval(t, `for x in a b c; do printf '%s.' "$x"; done`); got != "a.b.c." {
		t.Fatalf("for = %q", got)
	}
}

func TestForBraceBody(t *testing.T) {
	// bash brace-body form: { } stands in for do/done.
	if got := eval(t, `for x in a b c; { printf '%s.' "$x"; }`); got != "a.b.c." {
		t.Fatalf("for brace body = %q", got)
	}
	// Quoted array iteration with a brace body preserves elements with spaces.
	got := eval(t, `arr=(a "b c" d); for x in "${arr[@]}"; { echo "[$x]"; }`)
	if got != "[a]\n[b c]\n[d]" {
		t.Fatalf("brace body over array = %q", got)
	}
}

func TestWhileLoop(t *testing.T) {
	src := `i=0; while [ $i -lt 3 ]; do printf '%s' "$i"; i=$((i + 1)); done`
	if got := eval(t, src); got != "012" {
		t.Fatalf("while = %q", got)
	}
}

func TestUntilLoop(t *testing.T) {
	src := `i=0; until [ $i -ge 3 ]; do printf '%s' "$i"; i=$((i + 1)); done`
	if got := eval(t, src); got != "012" {
		t.Fatalf("until = %q", got)
	}
}

func TestCaseStatement(t *testing.T) {
	src := `for x in apple banana cherry; do case $x in a*) echo A;; b*) echo B;; *) echo other;; esac; done`
	if got := eval(t, src); got != "A\nB\nother" {
		t.Fatalf("case = %q", got)
	}
}

func TestBreakContinue(t *testing.T) {
	if got := eval(t, `for x in 1 2 3 4 5; do [ $x -eq 3 ] && break; printf '%s' "$x"; done`); got != "12" {
		t.Fatalf("break = %q", got)
	}
	if got := eval(t, `for x in 1 2 3 4; do [ $x -eq 2 ] && continue; printf '%s' "$x"; done`); got != "134" {
		t.Fatalf("continue = %q", got)
	}
}

func TestFunctionCallAndReturn(t *testing.T) {
	if got := eval(t, `greet() { echo "hi $1"; }; greet bob`); got != "hi bob" {
		t.Fatalf("function = %q", got)
	}
	if got := eval(t, `f() { return 7; }; f; echo $?`); got != "7" {
		t.Fatalf("return status = %q", got)
	}
}

func TestSubshellIsolatesVars(t *testing.T) {
	// A variable set inside ( ) does not leak to the parent.
	if got := eval(t, `X=outer; (X=inner); echo $X`); got != "outer" {
		t.Fatalf("subshell var leaked: %q", got)
	}
}

func TestGroupCmdSharesVars(t *testing.T) {
	// A variable set inside { } persists in the current shell.
	if got := eval(t, `{ X=grouped; }; echo $X`); got != "grouped" {
		t.Fatalf("group var did not persist: %q", got)
	}
}

func TestArithCommandStatus(t *testing.T) {
	// (( expr )) returns 0 when non-zero, 1 when zero.
	if got := eval(t, `(( 1 + 1 )) && echo nonzero`); got != "nonzero" {
		t.Fatalf("(( 1+1 )) = %q", got)
	}
	if got := eval(t, `(( 0 )) || echo zero`); got != "zero" {
		t.Fatalf("(( 0 )) = %q", got)
	}
}

// chdirTemp returns a fresh temp dir and switches the process into it,
// restoring the original cwd on cleanup.
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(orig) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir temp: %v", err)
	}
	return dir
}

func TestRedirectToFileAndBack(t *testing.T) {
	dir := chdirTemp(t)
	eval(t, `echo written > out.txt`)
	data, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatalf("reading redirected file: %v", err)
	}
	if strings.TrimSpace(string(data)) != "written" {
		t.Fatalf("redirected file content = %q", data)
	}
	// Read it back via input redirection.
	if got := eval(t, `cat < out.txt`); strings.TrimSpace(got) != "written" {
		t.Fatalf("input redirection = %q", got)
	}
}

func TestRedirectAppend(t *testing.T) {
	dir := chdirTemp(t)
	eval(t, `echo one > log.txt; echo two >> log.txt`)
	data, _ := os.ReadFile(filepath.Join(dir, "log.txt"))
	if strings.TrimSpace(string(data)) != "one\ntwo" {
		t.Fatalf("append content = %q", data)
	}
}

func TestStderrRedirect(t *testing.T) {
	dir := chdirTemp(t)
	// A failing command writes a diagnostic to stderr; capture it to a file.
	eval(t, `cat does_not_exist 2> err.txt`)
	data, _ := os.ReadFile(filepath.Join(dir, "err.txt"))
	if len(data) == 0 {
		t.Fatalf("expected stderr to be captured to file")
	}
}

func TestRedirectTargetExpansion(t *testing.T) {
	dir := chdirTemp(t)
	// Variable expansion in a redirect target.
	eval(t, `F=via_var.txt; echo hi > $F`)
	if data, err := os.ReadFile(filepath.Join(dir, "via_var.txt")); err != nil || strings.TrimSpace(string(data)) != "hi" {
		t.Fatalf("variable redirect target: data=%q err=%v", data, err)
	}
	// Quoted redirect target — quotes must be removed, not kept literally.
	eval(t, `echo hey > "quoted name.txt"`)
	if data, err := os.ReadFile(filepath.Join(dir, "quoted name.txt")); err != nil || strings.TrimSpace(string(data)) != "hey" {
		t.Fatalf("quoted redirect target: data=%q err=%v", data, err)
	}
}

func TestGlobExpansion(t *testing.T) {
	dir := chdirTemp(t)
	for _, n := range []string{"a.go", "b.go", "c.txt"} {
		os.WriteFile(filepath.Join(dir, n), nil, 0644)
	}
	if got := eval(t, `echo *.go`); got != "a.go b.go" {
		t.Fatalf("glob *.go = %q", got)
	}
}

func TestGlobNoMatchIsLiteral(t *testing.T) {
	chdirTemp(t)
	// No match: the pattern is left unchanged (bash default, no nullglob).
	if got := eval(t, `echo nomatch_*.xyz`); got != "nomatch_*.xyz" {
		t.Fatalf("unmatched glob = %q", got)
	}
}
