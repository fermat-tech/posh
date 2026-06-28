package eval

import "testing"

func TestSubstringExpansion(t *testing.T) {
	cases := map[string]string{
		`text="Hello World"; echo "${text:6:5}"`: "World",
		`text="Hello World"; echo "${text:6}"`:   "World",
		`text="Hello World"; echo "${text: -5}"`: "World", // negative offset
		`text="Hello World"; echo "${text:0:5}"`: "Hello",
		`text="Hello World"; echo "${text:0:-6}"`: "Hello", // negative length
		`text="Hello World"; i=6; n=5; echo "${text:i:n}"`: "World", // arithmetic
		`text="abc"; echo "[${text:10}]"`: "[]",  // offset past end
		`text="abc"; echo "[${text:1:0}]"`: "[]", // zero length
		`text="héllo"; echo "${text:1:3}"`: "éll", // multibyte by character
	}
	for src, want := range cases {
		if got := eval(t, src); got != want {
			t.Errorf("%s => %q, want %q", src, got, want)
		}
	}
}

func TestParamDefaultAndAssign(t *testing.T) {
	if got := eval(t, `echo "${U:-fallback}"`); got != "fallback" {
		t.Fatalf(":- = %q", got)
	}
	if got := eval(t, `X=set; echo "${X:-fallback}"`); got != "set" {
		t.Fatalf(":- with value = %q", got)
	}
	if got := eval(t, `X=set; echo "${X:+alt}"`); got != "alt" {
		t.Fatalf(":+ = %q", got)
	}
	// := assigns the default into the variable when unset.
	if got := eval(t, `echo "${V:=made}"; echo "got=$V"`); got != "made\ngot=made" {
		t.Fatalf(":= = %q", got)
	}
	// ${#var} counts characters.
	if got := eval(t, `s=héllo; echo "${#s}"`); got != "5" {
		t.Fatalf("length = %q", got)
	}
}
