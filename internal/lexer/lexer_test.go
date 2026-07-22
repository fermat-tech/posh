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

// TestTokenizeDoubleBracket covers [[ ]] recognition and the < / > rule
// inside it: they are ordinary comparison words, not redirect operators,
// matching bash's rule that they need not be quoted for use within [[ ]].
func TestTokenizeDoubleBracket(t *testing.T) {
	if got := types(`[[ -f a.txt ]]`); !eqTypes(got, DLBRACKET, WORD, WORD, RDLBRACKET) {
		t.Fatalf("got %v", got)
	}
	if got := types(`[[ $a < $b ]]`); !eqTypes(got, DLBRACKET, WORD, WORD, WORD, RDLBRACKET) {
		t.Fatalf("< inside [[ ]] should be an ordinary WORD, got %v", got)
	}
	if got := types(`[[ $a > $b ]]`); !eqTypes(got, DLBRACKET, WORD, WORD, WORD, RDLBRACKET) {
		t.Fatalf("> inside [[ ]] should be an ordinary WORD, got %v", got)
	}
	// Outside [[ ]], < and > are still ordinary redirects.
	if got := types(`cmd < in > out`); !eqTypes(got, WORD, REDIR_IN, WORD, REDIR_OUT, WORD) {
		t.Fatalf("< / > outside [[ ]] should still be redirects, got %v", got)
	}
	// && / || / ! / ( ) are tokenized normally within [[ ]] -- the PARSER
	// interprets them as the expression's own operators, not the lexer.
	if got := types(`[[ -f a && -f b ]]`); !eqTypes(got, DLBRACKET, WORD, WORD, AND, WORD, WORD, RDLBRACKET) {
		t.Fatalf("got %v", got)
	}
	// A bracket expression that isn't standalone [[ (e.g. glued to other
	// chars) is read as an ordinary word, matching how ((/{ are handled.
	if got := types(`echo a[[b`); !eqTypes(got, WORD, WORD) {
		t.Fatalf("non-standalone [[ should not become DLBRACKET, got %v", got)
	}
}

// TestTokenizeRegexOperandParens reproduces a bug found while implementing
// [[ ]]: bash relaxes ( ) and | (but only those, and only for the ONE word
// immediately following =~) so an unquoted extended regular expression with
// capture groups or alternation can be written without escaping, e.g.
// [[ $x =~ ^([a-z]+)([0-9]+)$ ]] or [[ $x =~ ^(cat|dog)$ ]]. Confirmed against
// a real bash 5.2 (bash -c) before fixing: bash accepts both of those
// unquoted, but still rejects an unquoted mid-word ( for other operators like
// == (see TestTokenizeParenStillWordStopForOtherOperators), and still rejects
// a bare & even in the regex operand (since a lone & is never valid inside
// [[ ]] regardless of position -- it's not specially relaxed alongside ( ) |).
func TestTokenizeRegexOperandParens(t *testing.T) {
	// The regex operand containing ( ) must read as ONE WORD, not split into
	// separate LPAREN/RPAREN tokens.
	got := types(`[[ $x =~ ^([a-z]+)([0-9]+)$ ]]`)
	if !eqTypes(got, DLBRACKET, WORD, WORD, WORD, RDLBRACKET) {
		t.Fatalf("regex operand with capture groups should be one WORD, got %v", got)
	}
	got = types(`[[ $x =~ ^(cat|dog)$ ]]`)
	if !eqTypes(got, DLBRACKET, WORD, WORD, WORD, RDLBRACKET) {
		t.Fatalf("regex operand with alternation should be one WORD, got %v", got)
	}
	// Only the word immediately after =~ is relaxed: a subsequent && still
	// works as the list-level operator, not swallowed into the regex.
	got = types(`[[ $x =~ ^(cat|dog)$ && 1 -eq 1 ]]`)
	want := []TokenType{DLBRACKET, WORD, WORD, WORD, AND, WORD, WORD, WORD, RDLBRACKET}
	if !eqTypes(got, want...) {
		t.Fatalf("&& after a regex operand should still be its own AND token, got %v want %v", got, want)
	}
	// A standalone ( for [[ ]] grouping is completely unaffected by this --
	// it's still its own LPAREN, since it's never immediately after =~.
	got = types(`[[ ( -f a.txt ) ]]`)
	if !eqTypes(got, DLBRACKET, LPAREN, WORD, WORD, RPAREN, RDLBRACKET) {
		t.Fatalf("grouping ( ) inside [[ ]] should be unaffected, got %v", got)
	}
}

// TestTokenizeParenStillWordStopForOtherOperators confirms the fix is scoped
// to exactly the =~ operand: bash (verified with bash -c) still rejects an
// unquoted mid-word ( for == (and everywhere else), so a WORD followed by (
// after == must still split into separate tokens, not merge into one word.
func TestTokenizeParenStillWordStopForOtherOperators(t *testing.T) {
	got := types(`[[ $x == foo(bar) ]]`)
	if eqTypes(got, DLBRACKET, WORD, WORD, WORD, RDLBRACKET) {
		t.Fatalf("== operand with ( should NOT be relaxed like =~ is, got %v", got)
	}
	// Outside [[ ]] entirely, ( is still the ordinary subshell/word-stop
	// token it always was -- regexOperandNext must never leak there.
	got = types(`echo foo(bar)`)
	if !eqTypes(got, WORD, WORD, LPAREN, WORD, RPAREN) {
		t.Fatalf("( outside [[ ]] should still stop the word, got %v", got)
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
