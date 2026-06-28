package parser

import (
	"testing"

	"github.com/fermat-tech/posh/internal/lexer"
)

func mustParse(t *testing.T, src string) Node {
	t.Helper()
	n, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse(%q) error: %v", src, err)
	}
	return n
}

// first returns the first command node, unwrapping a top-level *List if the
// parser produced one. (Parse returns the bare node when there are no list
// operators, and a *List otherwise.)
func first(t *testing.T, src string) Node {
	t.Helper()
	n := mustParse(t, src)
	if l, ok := n.(*List); ok {
		return l.First
	}
	return n
}

// pipeOf returns the *Pipeline for src, unwrapping a *List if present.
func pipeOf(t *testing.T, src string) *Pipeline {
	t.Helper()
	p, ok := first(t, src).(*Pipeline)
	if !ok {
		t.Fatalf("%q: want *Pipeline, got %T", src, first(t, src))
	}
	return p
}

func TestParseSimpleCommand(t *testing.T) {
	cmd, ok := pipeOf(t, "echo hello").Cmds[0].(*SimpleCmd)
	if !ok {
		t.Fatalf("want *SimpleCmd")
	}
	if len(cmd.Words) != 2 || cmd.Words[0] != "echo" || cmd.Words[1] != "hello" {
		t.Fatalf("got words %v", cmd.Words)
	}
}

func TestParsePipeline(t *testing.T) {
	if got := len(pipeOf(t, "a | b | c").Cmds); got != 3 {
		t.Fatalf("want 3 pipeline stages, got %d", got)
	}
}

func TestParseList(t *testing.T) {
	list := mustParse(t, "a && b || c ; d").(*List)
	if len(list.Elems) != 3 {
		t.Fatalf("want 3 list elems, got %d", len(list.Elems))
	}
	wantOps := []ListOp{OpAnd, OpOr, OpSemi}
	for i, op := range wantOps {
		if list.Elems[i].Op != op {
			t.Errorf("elem %d: want op %v, got %v", i, op, list.Elems[i].Op)
		}
	}
}

func TestParseBackground(t *testing.T) {
	list := mustParse(t, "sleep 1 &").(*List)
	if len(list.Elems) != 1 || list.Elems[0].Op != OpAmp {
		t.Fatalf("want trailing OpAmp, got %+v", list.Elems)
	}
}

func TestParseRedirections(t *testing.T) {
	cmd := pipeOf(t, "echo hi > out.txt").Cmds[0].(*SimpleCmd)
	if len(cmd.Redirs) != 1 || cmd.Redirs[0].Op != lexer.REDIR_OUT || cmd.Redirs[0].File != "out.txt" {
		t.Fatalf("got redirs %+v", cmd.Redirs)
	}
}

func TestParseSubshell(t *testing.T) {
	if _, ok := first(t, "(a; b)").(*Subshell); !ok {
		t.Fatalf("want *Subshell, got %T", first(t, "(a; b)"))
	}
}

func TestParseGroupCmd(t *testing.T) {
	if _, ok := first(t, "{ a; b; }").(*GroupCmd); !ok {
		t.Fatalf("want *GroupCmd, got %T", first(t, "{ a; b; }"))
	}
}

func TestParseIf(t *testing.T) {
	ifc, ok := first(t, "if true; then echo yes; elif false; then echo no; else echo maybe; fi").(*IfCmd)
	if !ok {
		t.Fatalf("want *IfCmd")
	}
	if len(ifc.Elifs) != 1 {
		t.Fatalf("want 1 elif, got %d", len(ifc.Elifs))
	}
	if ifc.Else == nil {
		t.Fatalf("want else branch")
	}
}

func TestParseFor(t *testing.T) {
	forc := first(t, "for x in a b c; do echo $x; done").(*ForCmd)
	if forc.Var != "x" {
		t.Fatalf("want loop var x, got %q", forc.Var)
	}
	if len(forc.Words) != 3 {
		t.Fatalf("want 3 words, got %v", forc.Words)
	}
}

func TestParseForBraceBody(t *testing.T) {
	// for ... ; { ... } is a bash extension where { } replaces do/done.
	forc, ok := first(t, "for x in a b c; { echo $x; }").(*ForCmd)
	if !ok {
		t.Fatalf("brace-body for did not parse to *ForCmd")
	}
	if forc.Var != "x" || len(forc.Words) != 3 {
		t.Fatalf("got var=%q words=%v", forc.Var, forc.Words)
	}
	if forc.Body == nil {
		t.Fatalf("brace-body for has no body")
	}
}

func TestParseForBraceBodyIncomplete(t *testing.T) {
	// An unterminated brace body should report incomplete (so the REPL keeps reading).
	if _, err := Parse("for x in a b; {"); err != ErrIncomplete {
		t.Fatalf("want ErrIncomplete for open brace body, got %v", err)
	}
}

func TestParseWhileUntil(t *testing.T) {
	if first(t, "while true; do echo x; done").(*WhileCmd).Until {
		t.Fatalf("while parsed as until")
	}
	if !first(t, "until false; do echo x; done").(*WhileCmd).Until {
		t.Fatalf("until not flagged")
	}
}

func TestParseCase(t *testing.T) {
	c := first(t, "case $x in a) echo A;; b|c) echo BC;; esac").(*CaseCmd)
	if len(c.Clauses) != 2 {
		t.Fatalf("want 2 clauses, got %d", len(c.Clauses))
	}
	if len(c.Clauses[1].Patterns) != 2 {
		t.Fatalf("want 2 patterns in clause 2, got %v", c.Clauses[1].Patterns)
	}
}

func TestParseFuncDefBothForms(t *testing.T) {
	if _, ok := first(t, "f() { echo hi; }").(*FuncDef); !ok {
		t.Fatalf("POSIX func form did not parse to FuncDef")
	}
	if _, ok := first(t, "function f { echo hi; }").(*FuncDef); !ok {
		t.Fatalf("keyword func form did not parse to FuncDef")
	}
}

func TestParseCompoundAsPipelineSource(t *testing.T) {
	// A subshell on the left of a pipe must be wrapped in a Pipeline.
	pipe := pipeOf(t, "(a; b) | c")
	if len(pipe.Cmds) != 2 {
		t.Fatalf("want 2 stages, got %d", len(pipe.Cmds))
	}
	if _, ok := pipe.Cmds[0].(*Subshell); !ok {
		t.Fatalf("first stage should be *Subshell, got %T", pipe.Cmds[0])
	}
}

func TestParseError(t *testing.T) {
	if _, err := Parse("if true"); err == nil {
		t.Fatalf("expected parse error for incomplete if")
	}
}

func TestNeedsContinuation(t *testing.T) {
	cont := []string{
		"echo \\",
		"a |",
		"a &&",
		"if true; then",
		"for x in a b; do",
		"{ echo hi",
		"(a; b",
		"cat << EOF\nbody",
	}
	for _, s := range cont {
		if !NeedsContinuation(s) {
			t.Errorf("NeedsContinuation(%q) = false, want true", s)
		}
	}
	done := []string{
		"echo hi",
		"if true; then echo x; fi",
		"for x in a; do echo $x; done",
		"cat << EOF\nbody\nEOF",
		"(a; b)",
		// Keywords used as ordinary words must NOT be treated as open blocks.
		"echo this is a multiline string for testing",
		"echo for testing",
		"echo if while until case done fi esac",
		"echo this is a \\\nmultiline string \\\nfor testing",
	}
	for _, s := range done {
		if NeedsContinuation(s) {
			t.Errorf("NeedsContinuation(%q) = true, want false", s)
		}
	}
}

func TestHeredocPending(t *testing.T) {
	if !heredocPending("cat << EOF\nline1") {
		t.Errorf("unterminated heredoc should be pending")
	}
	if heredocPending("cat << EOF\nline1\nEOF") {
		t.Errorf("terminated heredoc should not be pending")
	}
	// A << inside quotes is not a heredoc.
	if heredocPending(`echo "a << b"`) {
		t.Errorf("quoted << should not start a heredoc")
	}
	// <<< is a here-string, not a heredoc.
	if heredocPending("cat <<< word") {
		t.Errorf("here-string should not be pending")
	}
}
