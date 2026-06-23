package eval

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEchoFlags(t *testing.T) {
	if got := eval(t, `echo hello`); got != "hello" {
		t.Fatalf("echo = %q", got)
	}
	// -n suppresses the trailing newline (eval trims one, so compare raw).
	sh := New("posh")
	var out string
	out = evalRaw(sh, `echo -n hi`)
	if out != "hi" {
		t.Fatalf("echo -n = %q, want %q", out, "hi")
	}
}

func TestPrintf(t *testing.T) {
	if got := eval(t, `printf '%s-%s\n' a b`); got != "a-b" {
		t.Fatalf("printf = %q", got)
	}
	if got := eval(t, `printf '%d\n' 42`); got != "42" {
		t.Fatalf("printf %%d = %q", got)
	}
}

func TestExportEnvAndUnset(t *testing.T) {
	if got := eval(t, `export FOO=bar; env | grep '^FOO='`); got != "FOO=bar" {
		t.Fatalf("export/env = %q", got)
	}
	if got := eval(t, `FOO=bar; unset FOO; echo "[${FOO}]"`); got != "[]" {
		t.Fatalf("unset = %q", got)
	}
}

func TestAliasExpansion(t *testing.T) {
	if got := eval(t, `alias greet='echo hi'; greet`); got != "hi" {
		t.Fatalf("alias = %q", got)
	}
	if got := eval(t, `alias greet='echo hi'; unalias greet; greet 2>/dev/null; echo done`); got == "" {
		t.Fatalf("unalias produced no output")
	}
}

func TestCdAndPwd(t *testing.T) {
	tmp := t.TempDir()
	// Restore the process working directory before TempDir's cleanup runs,
	// otherwise Windows can't remove a directory that's still in use. Cleanups
	// run LIFO, so registering this after t.TempDir makes it run first.
	orig, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(orig) })

	// Resolve symlinks so macOS /var → /private/var doesn't trip the compare.
	real, _ := filepath.EvalSymlinks(tmp)
	got := eval(t, `cd "`+tmp+`"; pwd`)
	gotReal, _ := filepath.EvalSymlinks(got)
	if gotReal != real {
		t.Fatalf("pwd after cd = %q, want %q", gotReal, real)
	}
}

func TestTestBuiltinStringAndInt(t *testing.T) {
	cases := map[string]string{
		`test -z "" && echo yes`:      "yes",
		`test -n "x" && echo yes`:     "yes",
		`test abc = abc && echo yes`:  "yes",
		`test abc != xyz && echo yes`: "yes",
		`[ 3 -gt 2 ] && echo yes`:     "yes",
		`[ 2 -lt 2 ] || echo no`:      "no",
		`[ 5 -eq 5 ] && echo yes`:     "yes",
	}
	for src, want := range cases {
		if got := eval(t, src); got != want {
			t.Errorf("%s => %q, want %q", src, got, want)
		}
	}
}

func TestTestFilePredicates(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f.txt")
	os.WriteFile(f, []byte("hi"), 0644)
	if got := eval(t, `[ -f "`+f+`" ] && echo yes`); got != "yes" {
		t.Fatalf("-f on file = %q", got)
	}
	if got := eval(t, `[ -d "`+dir+`" ] && echo yes`); got != "yes" {
		t.Fatalf("-d on dir = %q", got)
	}
	if got := eval(t, `[ -e "`+dir+`/nope" ] || echo absent`); got != "absent" {
		t.Fatalf("-e on missing = %q", got)
	}
}

func TestTrueFalseColon(t *testing.T) {
	if got := eval(t, `true; echo $?`); got != "0" {
		t.Fatalf("true status = %q", got)
	}
	if got := eval(t, `false; echo $?`); got != "1" {
		t.Fatalf("false status = %q", got)
	}
	if got := eval(t, `:; echo $?`); got != "0" {
		t.Fatalf(": status = %q", got)
	}
}

func TestTypeBuiltin(t *testing.T) {
	if got := eval(t, `type cd`); got == "" {
		t.Fatalf("type cd produced no output")
	}
}

func TestEvalBuiltin(t *testing.T) {
	if got := eval(t, `eval 'echo from eval'`); got != "from eval" {
		t.Fatalf("eval builtin = %q", got)
	}
}

func TestShiftBuiltin(t *testing.T) {
	if got := eval(t, `f() { shift; echo "$1"; }; f a b c`); got != "b" {
		t.Fatalf("shift in function = %q", got)
	}
}
