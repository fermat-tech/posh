package eval

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

// TestUnderscoreVar covers $_, the last word of the previously run command.
func TestUnderscoreVar(t *testing.T) {
	if got := eval(t, `echo a b c; echo "$_"`); got != "a b c\nc" {
		t.Fatalf("$_ = %q", got)
	}
}

// TestOptFlagsVar covers $-: the bash single-letter mnemonic for each
// currently-enabled `set -o` option that has one, in bash's canonical order.
func TestOptFlagsVar(t *testing.T) {
	if got := eval(t, `set -o xtrace; set -o nounset; echo "$-"`); got != "ux" {
		t.Fatalf("$- = %q, want %q", got, "ux")
	}
	if got := eval(t, `echo "$-"`); got != "" {
		t.Fatalf("$- with no options set = %q, want empty", got)
	}
}

// TestBackgroundPIDVar reproduces a bug caught while implementing $!: it was
// written into a dedicated field but read from a different one entirely, so
// it was always empty. A process-backed background job's real PID must be
// visible via $!; a goroutine-backed one (a backgrounded compound command,
// which has no real OS process) must leave it empty rather than stale.
func TestBackgroundPIDVar(t *testing.T) {
	sh := New("posh")
	var buf bytes.Buffer
	sh.Stdout, sh.Stderr = &buf, &buf

	if code := sh.EvalString("sleep 2 &"); code != 0 {
		t.Fatalf("backgrounding statement returned %d, want 0", code)
	}
	pid := sh.GetVar("!")
	if pid == "" {
		t.Fatal("$! is empty after backgrounding a real external command")
	}
	if _, err := strconv.Atoi(pid); err != nil {
		t.Fatalf("$! = %q, want a numeric PID", pid)
	}
	// Clean up rather than waiting out the full sleep.
	for _, j := range sh.jobs.list() {
		j.RequestStop(nil)
	}

	buf.Reset()
	if code := sh.EvalString("(sleep 1) &"); code != 0 {
		t.Fatalf("backgrounding statement returned %d, want 0", code)
	}
	if got := sh.GetVar("!"); got != "" {
		t.Fatalf("$! after backgrounding a compound command = %q, want empty (no real process)", got)
	}
	for _, j := range sh.jobs.list() {
		j.RequestStop(nil)
	}
}

// TestOldPwdAndCdDash covers $OLDPWD and `cd -`.
func TestOldPwdAndCdDash(t *testing.T) {
	sh := New("posh")
	start := sh.GetVar("PWD")
	tmp := t.TempDir()

	if got := eval2(sh, t, "cd "+shellSingleQuote(tmp)); got != "" {
		t.Fatalf("cd = %q, want no output", got)
	}
	if got := sh.GetVar("OLDPWD"); got != start {
		t.Fatalf("OLDPWD = %q, want %q", got, start)
	}

	got := eval2(sh, t, "cd -")
	if strings.TrimSpace(got) == "" {
		t.Fatal("cd - printed nothing, want the resulting directory")
	}
	if newPwd := sh.GetVar("PWD"); newPwd != start {
		t.Fatalf("PWD after cd - = %q, want %q (back to where we started)", newPwd, start)
	}
}

// TestRandomVarIsDynamic covers $RANDOM: a fresh value on every reference,
// not a stored one.
func TestRandomVarIsDynamic(t *testing.T) {
	got := eval(t, `echo "$RANDOM $RANDOM $RANDOM"`)
	parts := strings.Fields(got)
	if len(parts) != 3 {
		t.Fatalf("$RANDOM $RANDOM $RANDOM = %q, want 3 fields", got)
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 32767 {
			t.Fatalf("RANDOM value %q out of range [0,32767]", p)
		}
	}
	// Not a hard guarantee (three draws COULD coincide), but if all three
	// match every run something is almost certainly wrong (e.g. RANDOM
	// silently resolving to a stored, unchanging value).
	if parts[0] == parts[1] && parts[1] == parts[2] {
		t.Errorf("all three $RANDOM draws were identical (%s) -- suspicious", parts[0])
	}
}

// TestSecondsVarResets covers $SECONDS and the SECONDS=n reset idiom. The
// direct-write bug this guards against: applyAssign (VAR=val) used to write
// straight into the vars map, bypassing setVar's SECONDS handling entirely,
// so SECONDS=n was silently a no-op.
func TestSecondsVarResets(t *testing.T) {
	sh := New("posh")
	if got := sh.GetVar("SECONDS"); got != "0" {
		t.Fatalf("SECONDS immediately after New() = %q, want \"0\"", got)
	}
	evalRaw(sh, "SECONDS=100")
	if got := sh.GetVar("SECONDS"); got != "100" {
		t.Fatalf("SECONDS after SECONDS=100 = %q, want \"100\"", got)
	}
}

// TestPoshPIDVar covers $POSHPID (posh's name for bash's $BASHPID): it must
// be numeric and equal to $$, since posh's ( ... ) subshells run as an
// in-process goroutine rather than a real forked OS process.
func TestPoshPIDVar(t *testing.T) {
	got := eval(t, `echo "$$ $POSHPID"`)
	parts := strings.Fields(got)
	if len(parts) != 2 || parts[0] != parts[1] {
		t.Fatalf("$$ $POSHPID = %q, want two identical values", got)
	}
}

// TestSynthesizedEnvVars covers USER/HOSTNAME/SHELL/PWD/IFS, which posh
// synthesizes at startup since Windows doesn't set the bash-standard names
// (USER/HOSTNAME/SHELL) and PWD/IFS were previously only set lazily (PWD only
// after the first cd; IFS only implicitly, inside wordSplit).
func TestSynthesizedEnvVars(t *testing.T) {
	sh := New("posh")
	for _, name := range []string{"USER", "HOSTNAME", "SHELL", "PWD"} {
		if sh.GetVar(name) == "" {
			t.Errorf("%s is empty immediately after New()", name)
		}
	}
	if got := sh.GetVar("IFS"); got != " \t\n" {
		t.Fatalf("IFS = %q, want the default whitespace set", got)
	}
}

// eval2 is like eval but reuses an existing Shell instead of creating a fresh
// one, for tests that need to observe state (PWD, OLDPWD, ...) across calls.
func eval2(sh *Shell, t *testing.T, src string) string {
	t.Helper()
	var buf bytes.Buffer
	sh.Stdout = &buf
	sh.Stderr = &buf
	sh.EvalString(src)
	out := strings.ReplaceAll(buf.String(), "\r", "")
	return strings.TrimRight(out, "\n")
}
