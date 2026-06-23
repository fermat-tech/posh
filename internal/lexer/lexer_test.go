package lexer

import "testing"

// types tokenizes input and returns the token types (without the trailing EOF).
func types(input string) []TokenType {
	toks := New(input).Tokenize()
	if len(toks) > 0 && toks[len(toks)-1].Type == EOF {
		toks = toks[:len(toks)-1]
	}
	out := make([]TokenType, len(toks))
	for i, t := range toks {
		out[i] = t.Type
	}
	return out
}

func eqTypes(a []TokenType, b ...TokenType) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTokenizeSimpleCommand(t *testing.T) {
	got := types("echo hello world")
	if !eqTypes(got, WORD, WORD, WORD) {
		t.Fatalf("got %v", got)
	}
}

func TestTokenizeOperators(t *testing.T) {
	cases := map[string]TokenType{
		"|":   PIPE,
		"&&":  AND,
		"||":  OR,
		";":   SEMI,
		"&":   AMP,
		">":   REDIR_OUT,
		">>":  REDIR_APPEND,
		"<":   REDIR_IN,
		"2>":  REDIR_ERR,
		"2>>": REDIR_ERR_APPEND,
		"&>":  REDIR_BOTH,
		"<<":  HEREDOC_OP,
		"<<-": HEREDOC_STRIP_OP,
		"<<<": HERESTRING_OP,
	}
	for in, want := range cases {
		// Surround with words so position-sensitive lexing is realistic.
		toks := New("a " + in + " b").Tokenize()
		found := false
		for _, tk := range toks {
			if tk.Type == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q: expected token %v, got %v", in, want, types("a "+in+" b"))
		}
	}
}

func TestTokenizeAssignment(t *testing.T) {
	got := types("FOO=bar")
	if !eqTypes(got, ASSIGN) {
		t.Fatalf("got %v", got)
	}
	// Not an assignment when not in command position.
	got = types("echo FOO=bar")
	if !eqTypes(got, WORD, WORD) {
		t.Fatalf("got %v", got)
	}
}

func TestTokenizeDupRedir(t *testing.T) {
	toks := New("cmd 2>&1").Tokenize()
	var dup *Token
	for i := range toks {
		if toks[i].Type == REDIR_DUP_OUT {
			dup = &toks[i]
		}
	}
	if dup == nil {
		t.Fatalf("no REDIR_DUP_OUT in %v", types("cmd 2>&1"))
	}
	if dup.Val != "2:1" {
		t.Fatalf("want Val 2:1, got %q", dup.Val)
	}
}

func TestTokenizeArithCommand(t *testing.T) {
	toks := New("(( 1 + 2 ))").Tokenize()
	if toks[0].Type != DLARITH {
		t.Fatalf("want DLARITH, got %v", toks[0].Type)
	}
	if toks[0].Val != "1 + 2" {
		t.Fatalf("want expr '1 + 2', got %q", toks[0].Val)
	}
}

func TestTokenizeSubshellVsArith(t *testing.T) {
	if got := types("( a )"); !eqTypes(got, LPAREN, WORD, RPAREN) {
		t.Fatalf("subshell parens: got %v", got)
	}
}

func TestTokenizeComment(t *testing.T) {
	if got := types("echo hi # trailing"); !eqTypes(got, WORD, WORD) {
		t.Fatalf("got %v", got)
	}
}

func TestTokenizeQuotedWord(t *testing.T) {
	// A quoted string is a single WORD token.
	if got := types(`echo "hello world"`); !eqTypes(got, WORD, WORD) {
		t.Fatalf("got %v", got)
	}
	if got := types(`echo 'a b c'`); !eqTypes(got, WORD, WORD) {
		t.Fatalf("got %v", got)
	}
}

func TestUnterminatedQuoteRecorded(t *testing.T) {
	l := New(`echo "unterminated`)
	l.Tokenize()
	if len(l.Errors) == 0 {
		t.Fatalf("expected an unterminated-string error")
	}
}

func TestNewlineAndPipeReset(t *testing.T) {
	// After a newline, an assignment-looking word is an ASSIGN again.
	if got := types("echo x\nFOO=bar"); !eqTypes(got, WORD, WORD, NEWLINE, ASSIGN) {
		t.Fatalf("got %v", got)
	}
}

func TestIsAssignment(t *testing.T) {
	cases := map[string]bool{
		"FOO=bar":  true,
		"_x=1":     true,
		"a1=2":     true,
		"=bad":     false,
		"1bad=2":   false,
		"noequals": false,
		"a b=c":    false,
	}
	for in, want := range cases {
		if got := isAssignment(in); got != want {
			t.Errorf("isAssignment(%q) = %v, want %v", in, got, want)
		}
	}
}
