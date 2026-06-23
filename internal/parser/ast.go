// Package parser defines the posh AST and recursive-descent parser.
package parser

import "github.com/fermat-tech/posh/internal/lexer"

// Node is the common interface for all AST nodes.
type Node interface {
	nodeTag()
}

// Redir represents a single I/O redirection on a command.
type Redir struct {
	Op    lexer.TokenType
	File  string // target filename; for HEREDOC_OP this holds the content
	Delim string // heredoc delimiter (for <<- strip mode)
	Strip bool   // true for <<-
	// Fd1, Fd2 carry fd numbers for the generalised redirect ops:
	//   REDIR_FD_OUT / FD_APPEND / FD_IN  →  Fd1 = N  (the "N" in N>file)
	//   REDIR_DUP_OUT / DUP_IN            →  Fd1 = src, Fd2 = dst  (N>&M)
	//   REDIR_CLOSE_OUT / CLOSE_IN        →  Fd1 = fd to close
	Fd1 int
	Fd2 int
}

// SimpleCmd is a single external command or built-in invocation.
type SimpleCmd struct {
	Assigns []string // "VAR=val"
	Words   []string // command + args (unexpanded; evaluator expands them)
	Redirs  []Redir
}

func (*SimpleCmd) nodeTag() {}

// Pipeline is one or more commands connected by |.
// Each element may be a SimpleCmd or a compound command.
// Negate inverts the exit status (prefix !).
type Pipeline struct {
	Cmds   []Node
	Negate bool
}

func (*Pipeline) nodeTag() {}

// ListOp is the operator between two consecutive pipelines in a List.
type ListOp int

const (
	OpSemi ListOp = iota // ;  (or newline)
	OpAnd                // &&
	OpOr                 // ||
	OpAmp                // & (background)
)

// ListElem is one element of a List: an operator + the node that follows it.
type ListElem struct {
	Op   ListOp
	Node Node // may be nil for trailing &
}

// List is a sequence of nodes joined by ;  &&  ||  &.
type List struct {
	First Node
	Elems []ListElem
}

func (*List) nodeTag() {}

// Subshell executes a List in a sub-environment.
type Subshell struct {
	Body  *List
	Redirs []Redir
}

func (*Subshell) nodeTag() {}

// GroupCmd executes a List in the current shell environment { list; }
type GroupCmd struct {
	Body   *List
	Redirs []Redir
}

func (*GroupCmd) nodeTag() {}

// ---- compound commands ----

// ElifClause is one elif branch.
type ElifClause struct {
	Cond *List
	Then *List
}

// IfCmd implements if/elif/else/fi.
type IfCmd struct {
	Cond   *List
	Then   *List
	Elifs  []ElifClause
	Else   *List
	Redirs []Redir
}

func (*IfCmd) nodeTag() {}

// ForCmd implements for VAR in WORDS; do BODY; done.
// If Words is nil, iterate over positional parameters.
type ForCmd struct {
	Var    string
	Words  []string // unexpanded; evaluator expands
	Body   *List
	Redirs []Redir
}

func (*ForCmd) nodeTag() {}

// WhileCmd implements while/until COND; do BODY; done.
type WhileCmd struct {
	Cond   *List
	Body   *List
	Until  bool // true for 'until' (invert cond test)
	Redirs []Redir
}

func (*WhileCmd) nodeTag() {}

// CaseClause is one arm of a case statement.
type CaseClause struct {
	Patterns []string // shell glob patterns
	Body     *List    // may be nil
}

// CaseCmd implements case WORD in CLAUSES esac.
type CaseCmd struct {
	Word    string
	Clauses []CaseClause
	Redirs  []Redir
}

func (*CaseCmd) nodeTag() {}

// FuncDef defines a shell function.
type FuncDef struct {
	Name string
	Body Node // GroupCmd or any compound command
}

func (*FuncDef) nodeTag() {}

// ArithCmd is a standalone (( expr )) arithmetic command.
// Returns exit code 0 if expr is non-zero, 1 if zero.
type ArithCmd struct {
	Expr string
}

func (*ArithCmd) nodeTag() {}
