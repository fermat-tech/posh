package eval

import (
	"strings"
	"testing"
)

// run evaluates src in a fresh shell, capturing stdout. A top-level `exit`
// surfaces as a shellExit panic; run recovers it and reports the exit code and
// exited=true, mirroring what main's top-level recover does.
func run(t *testing.T, src string) (out string, code int, exited bool) {
	t.Helper()
	sh := New("posh")
	buf := &syncBuf{}
	sh.Stdout = buf
	sh.Stderr = buf

	defer func() {
		if r := recover(); r != nil {
			if c, ok := ExitCode(r); ok {
				out, code, exited = buf.String(), c, true
				return
			}
			panic(r)
		}
	}()
	code = sh.EvalString(src)
	return buf.String(), code, false
}

func TestExitInSubshellIsContained(t *testing.T) {
	out, _, exited := run(t, `(exit 7); echo "after=$?"`)
	if exited {
		t.Fatalf("subshell exit escaped to the session")
	}
	if !strings.Contains(out, "after=7") {
		t.Fatalf("want $? = 7 after subshell, got %q", out)
	}
}

func TestExitInCommandSubstitutionIsContained(t *testing.T) {
	out, _, exited := run(t, `x=$(exit 9; echo hi); echo "alive x=[$x]"`)
	if exited {
		t.Fatalf("command-substitution exit escaped to the session")
	}
	if !strings.Contains(out, "alive x=[]") {
		t.Fatalf("expected substitution to stop at exit and parent to survive, got %q", out)
	}
}

func TestExitInPipelineStageIsContained(t *testing.T) {
	out, _, exited := run(t, `exit 5 | cat; echo done`)
	if exited {
		t.Fatalf("pipeline-stage exit escaped to the session")
	}
	if !strings.Contains(out, "done") {
		t.Fatalf("expected pipeline to complete, got %q", out)
	}
}

func TestTopLevelExitEndsSession(t *testing.T) {
	out, code, exited := run(t, `echo before; exit 3; echo after`)
	if !exited {
		t.Fatalf("top-level exit did not end the session")
	}
	if code != 3 {
		t.Fatalf("want exit code 3, got %d", code)
	}
	if strings.Contains(out, "after") {
		t.Fatalf("commands after exit should not run, got %q", out)
	}
	if !strings.Contains(out, "before") {
		t.Fatalf("commands before exit should run, got %q", out)
	}
}

func TestExitNoArgUsesLastStatus(t *testing.T) {
	_, code, exited := run(t, `false; exit`)
	if !exited || code != 1 {
		t.Fatalf("want exit using $?=1, got code=%d exited=%v", code, exited)
	}
}

func TestExitInFunctionEndsSession(t *testing.T) {
	// In bash, `exit` inside a function terminates the shell, not just the call.
	out, code, exited := run(t, `f() { exit 4; }; echo start; f; echo end`)
	if !exited {
		t.Fatalf("exit in function should end the session")
	}
	if code != 4 {
		t.Fatalf("want exit code 4, got %d", code)
	}
	if strings.Contains(out, "end") {
		t.Fatalf("command after function-exit should not run, got %q", out)
	}
}
