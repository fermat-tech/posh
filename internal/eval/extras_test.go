package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPositionalParamsInFunction(t *testing.T) {
	if got := eval(t, `f() { echo "$1-$2-$#"; }; f a b`); got != "a-b-2" {
		t.Fatalf("positional params = %q", got)
	}
}

func TestNestedCommandSubstitution(t *testing.T) {
	if got := eval(t, `echo $(echo $(echo deep))`); got != "deep" {
		t.Fatalf("nested cmdsub = %q", got)
	}
}

func TestCombinedStdoutStderrRedirect(t *testing.T) {
	dir := chdirTemp(t)
	eval(t, `cat does_not_exist &> both.txt`)
	data, _ := os.ReadFile(filepath.Join(dir, "both.txt"))
	if len(data) == 0 {
		t.Fatalf("&> did not capture output")
	}
}

func TestSetOptionViAndBack(t *testing.T) {
	sh := New("posh")
	evalRaw(sh, `set -o vi`)
	if !sh.GetOpt("vi") {
		t.Fatalf("set -o vi did not enable the vi option")
	}
	evalRaw(sh, `set +o vi`)
	if sh.GetOpt("vi") {
		t.Fatalf("set +o vi did not disable the vi option")
	}
}

func TestReadBuiltin(t *testing.T) {
	sh := New("posh")
	sh.Stdin = strings.NewReader("hello world\n")
	out := evalRaw(sh, `read line; echo "got:$line"`)
	if strings.TrimSpace(out) != "got:hello world" {
		t.Fatalf("read = %q", out)
	}
}

func TestExitStatusOfPipelineIsLastStage(t *testing.T) {
	if got := eval(t, `true | false; echo $?`); got != "1" {
		t.Fatalf("pipeline status = %q (want last stage)", got)
	}
	if got := eval(t, `false | true; echo $?`); got != "0" {
		t.Fatalf("pipeline status = %q (want last stage)", got)
	}
}

func TestVarDefaultEmpty(t *testing.T) {
	if got := eval(t, `echo "[${UNDEFINED_VAR}]"`); got != "[]" {
		t.Fatalf("undefined var = %q", got)
	}
}

func TestMultipleAssignmentsBeforeCommand(t *testing.T) {
	// VAR=val on the same line as a command applies for that command's env.
	if got := eval(t, `A=1; B=2; echo "$A$B"`); got != "12" {
		t.Fatalf("assignments = %q", got)
	}
}

func TestForOverPositionalParams(t *testing.T) {
	// `for x; do ...` with no `in` iterates positional parameters.
	if got := eval(t, `f() { for a; do printf '%s.' "$a"; done; }; f x y z`); got != "x.y.z." {
		t.Fatalf("for over params = %q", got)
	}
}
