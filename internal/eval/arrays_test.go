package eval

import "testing"

func TestArrayLiteralAndIndex(t *testing.T) {
	if got := eval(t, `arr=(Groucho Chico Harpo); echo "${arr[0]} ${arr[2]}"`); got != "Groucho Harpo" {
		t.Fatalf("index = %q", got)
	}
}

func TestArrayAll(t *testing.T) {
	if got := eval(t, `arr=(a b c); echo "${arr[@]}"`); got != "a b c" {
		t.Fatalf("[@] = %q", got)
	}
	if got := eval(t, `arr=(a b c); echo "${arr[*]}"`); got != "a b c" {
		t.Fatalf("[*] = %q", got)
	}
}

func TestArrayLength(t *testing.T) {
	if got := eval(t, `arr=(a b c d); echo "${#arr[@]}"`); got != "4" {
		t.Fatalf("length = %q", got)
	}
	if got := eval(t, `arr=(); echo "${#arr[@]}"`); got != "0" {
		t.Fatalf("empty length = %q", got)
	}
}

func TestArrayElementLength(t *testing.T) {
	if got := eval(t, `arr=(abc de); echo "${#arr[0]}"`); got != "3" {
		t.Fatalf("element length = %q", got)
	}
}

func TestArrayBareNameIsElementZero(t *testing.T) {
	if got := eval(t, `arr=(first second); echo "$arr"`); got != "first" {
		t.Fatalf("$arr = %q", got)
	}
}

func TestArrayElementAssignment(t *testing.T) {
	if got := eval(t, `arr=(a b c); arr[1]=X; echo "${arr[@]}"`); got != "a X c" {
		t.Fatalf("element assign = %q", got)
	}
}

func TestArrayElementAssignmentCreates(t *testing.T) {
	// Assigning an element to a non-existent name creates an array.
	if got := eval(t, `a[2]=hi; echo "${a[2]}|${#a[@]}"`); got != "hi|3" {
		t.Fatalf("create via element = %q", got)
	}
}

func TestArrayAppend(t *testing.T) {
	if got := eval(t, `arr=(a b); arr+=(c d); echo "${arr[@]}|${#arr[@]}"`); got != "a b c d|4" {
		t.Fatalf("append = %q", got)
	}
}

func TestArrayNegativeIndex(t *testing.T) {
	if got := eval(t, `arr=(a b c); echo "${arr[-1]}"`); got != "c" {
		t.Fatalf("negative index = %q", got)
	}
}

func TestArrayIndices(t *testing.T) {
	if got := eval(t, `arr=(a b c); echo "${!arr[@]}"`); got != "0 1 2" {
		t.Fatalf("indices = %q", got)
	}
}

func TestArrayQuotedElementsWithSpaces(t *testing.T) {
	if got := eval(t, `arr=("a b" "c d"); echo "${#arr[@]}:${arr[0]}:${arr[1]}"`); got != "2:a b:c d" {
		t.Fatalf("quoted elements = %q", got)
	}
}

func TestArrayIterationPreservesElements(t *testing.T) {
	// Quoted [@] yields one word per element, even with embedded spaces.
	got := eval(t, `arr=("a b" c); for x in "${arr[@]}"; do echo "[$x]"; done`)
	if got != "[a b]\n[c]" {
		t.Fatalf("iteration = %q", got)
	}
}

func TestArrayUnsetElement(t *testing.T) {
	if got := eval(t, `arr=(a b c d); unset "arr[1]"; echo "${arr[@]}|${#arr[@]}"`); got != "a c d|3" {
		t.Fatalf("unset element = %q", got)
	}
}

func TestArrayUnsetWhole(t *testing.T) {
	if got := eval(t, `arr=(a b c); unset arr; echo "[${arr[@]}]"`); got != "[]" {
		t.Fatalf("unset whole = %q", got)
	}
}

func TestArrayIndexExpression(t *testing.T) {
	// Subscripts may be arithmetic over variables.
	if got := eval(t, `arr=(a b c d); i=1; echo "${arr[i+1]}"`); got != "c" {
		t.Fatalf("index expression = %q", got)
	}
}

func TestScalarAssignmentStillWorks(t *testing.T) {
	if got := eval(t, `x=5; x+=2; name=world; echo "$x $name"`); got != "52 world" {
		t.Fatalf("scalar = %q", got)
	}
}

// ---- associative arrays ----

func TestAssocInlineInitAndValue(t *testing.T) {
	src := `declare -A roles=([admin]="rw" [guest]="r"); echo "${roles[admin]}|${roles[guest]}"`
	if got := eval(t, src); got != "rw|r" {
		t.Fatalf("assoc value = %q", got)
	}
}

func TestAssocLength(t *testing.T) {
	if got := eval(t, `declare -A m=([a]=1 [b]=2 [c]=3); echo "${#m[@]}"`); got != "3" {
		t.Fatalf("assoc length = %q", got)
	}
}

func TestAssocElementLength(t *testing.T) {
	if got := eval(t, `declare -A m=([k]=hello); echo "${#m[k]}"`); got != "5" {
		t.Fatalf("assoc element length = %q", got)
	}
}

func TestAssocElementAssignment(t *testing.T) {
	if got := eval(t, `declare -A m; m[x]=1; m[y]=2; echo "${m[x]}${m[y]}"`); got != "12" {
		t.Fatalf("assoc element assign = %q", got)
	}
}

func TestAssocKeysSorted(t *testing.T) {
	// Keys come back in a stable (sorted) order.
	if got := eval(t, `declare -A m=([b]=2 [a]=1 [c]=3); echo "${!m[@]}"`); got != "a b c" {
		t.Fatalf("assoc keys = %q", got)
	}
	if got := eval(t, `declare -A m=([b]=2 [a]=1 [c]=3); echo "${m[@]}"`); got != "1 2 3" {
		t.Fatalf("assoc values = %q", got)
	}
}

func TestAssocIterate(t *testing.T) {
	src := `declare -A c=([red]=apple [green]=lime); for k in "${!c[@]}"; do echo "$k=${c[$k]}"; done`
	if got := eval(t, src); got != "green=lime\nred=apple" {
		t.Fatalf("assoc iteration = %q", got)
	}
}

func TestAssocKeyWithSpace(t *testing.T) {
	if got := eval(t, `declare -A m; m["a b"]=val; echo "${m[a b]}"`); got != "val" {
		t.Fatalf("assoc key with space = %q", got)
	}
}

func TestAssocUnsetKey(t *testing.T) {
	if got := eval(t, `declare -A m=([a]=1 [b]=2); unset "m[a]"; echo "${!m[@]}|${#m[@]}"`); got != "b|1" {
		t.Fatalf("assoc unset = %q", got)
	}
}

func TestParseAssignParts(t *testing.T) {
	cases := []struct {
		in        string
		name, sub string
		hasSub    bool
		app       bool
		value     string
	}{
		{"x=1", "x", "", false, false, "1"},
		{"x+=1", "x", "", false, true, "1"},
		{"a[2]=v", "a", "2", true, false, "v"},
		{"a[i+1]+=v", "a", "i+1", true, true, "v"},
		{"arr=(a b)", "arr", "", false, false, "(a b)"},
	}
	for _, c := range cases {
		ap, ok := parseAssignParts(c.in)
		if !ok {
			t.Errorf("parseAssignParts(%q) failed", c.in)
			continue
		}
		if ap.name != c.name || ap.sub != c.sub || ap.hasSub != c.hasSub || ap.append != c.app || ap.value != c.value {
			t.Errorf("parseAssignParts(%q) = %+v, want name=%q sub=%q hasSub=%v app=%v value=%q",
				c.in, ap, c.name, c.sub, c.hasSub, c.app, c.value)
		}
	}
}
