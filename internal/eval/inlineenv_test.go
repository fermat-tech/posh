package eval

import (
	"sort"
	"strings"
	"testing"
)

// envValue returns the value for key in an environment slice, and whether it was
// present exactly once.
func envValue(env []string, key string) (string, int) {
	val, count := "", 0
	for _, kv := range env {
		if eq := strings.IndexByte(kv, '='); eq > 0 && kv[:eq] == key {
			val = kv[eq+1:]
			count++
		}
	}
	return val, count
}

func TestInlineEnvOverridesExisting(t *testing.T) {
	sh := New("posh")
	base := []string{"TZ=America/Chicago", "PATH=/bin"}
	// `TZ= date` — empty assignment must replace the inherited TZ, not duplicate it.
	got := sh.inlineEnv(base, []string{"TZ="})
	val, count := envValue(got, "TZ")
	if count != 1 {
		t.Fatalf("TZ appears %d times, want exactly 1 (no duplicates)", count)
	}
	if val != "" {
		t.Fatalf("TZ = %q, want empty", val)
	}
	// Unrelated entries are preserved.
	if v, _ := envValue(got, "PATH"); v != "/bin" {
		t.Fatalf("PATH = %q, want /bin", v)
	}
}

func TestInlineEnvAddsFresh(t *testing.T) {
	sh := New("posh")
	got := sh.inlineEnv([]string{"PATH=/bin"}, []string{"FOO=bar"})
	if v, c := envValue(got, "FOO"); v != "bar" || c != 1 {
		t.Fatalf("FOO = %q (count %d), want bar (1)", v, c)
	}
}

func TestInlineEnvAppend(t *testing.T) {
	sh := New("posh")
	got := sh.inlineEnv([]string{"P=base"}, []string{"P+=/more"})
	if v, c := envValue(got, "P"); v != "base/more" || c != 1 {
		t.Fatalf("P = %q (count %d), want base/more (1)", v, c)
	}
}

func TestInlineEnvNoAssignsReturnsBase(t *testing.T) {
	sh := New("posh")
	base := []string{"A=1", "B=2"}
	got := sh.inlineEnv(base, nil)
	sort.Strings(got)
	if strings.Join(got, ",") != "A=1,B=2" {
		t.Fatalf("no-assigns env = %v", got)
	}
}

// A command prefix in front of an alias must reach the aliased command's
// environment (the env builtin prints the exported environment of its shell).
func TestAliasCarriesPrefixAssignment(t *testing.T) {
	if got := eval(t, `alias e=env; VARX=hello e`); !strings.Contains(got, "VARX=hello") {
		t.Fatalf("alias did not carry prefix assignment; env = %q", got)
	}
}

// A command prefix in front of a shell function reaches the function's
// environment too.
func TestFunctionCarriesPrefixAssignment(t *testing.T) {
	if got := eval(t, `f() { env; }; VARY=world f`); !strings.Contains(got, "VARY=world") {
		t.Fatalf("function did not carry prefix assignment; env = %q", got)
	}
}
