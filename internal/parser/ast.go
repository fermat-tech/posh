// Package parser defines the posh AST and recursive-descent parser.
package parser

import "github.com/fermat-tech/posh/internal/lexer"

// Node is the common interface for all AST nodes.
type Node interface {
	nodeTag()
}

// Redir represents a single I/O redirection on a command.
type Redir struct {
	Op   lexer.TokenType // REDIR_OUT, REDIR_APPEND, REDIR_IN, REDIR_ERR, REDIR_ERR_APPEND, REDIR_BOTH
	File string          // target filename (after expansion)
}

// SimpleCmd is a single external command or built-in invocation.
//
//	assignments: VAR=val pairs prepended before the command
//	words[0]:    command name; words[1:]: arguments
type SimpleCmd struct {
	Assigns  []string // "VAR=val"
	Words    []string // command + args (unexpanded; evaluator expands them)
	Redirs   []Redir
}

func (*SimpleCmd) nodeTag() {}

// Pipeline is one or more SimpleCmds connected by |.
// Negate inverts the exit status (prefix !).
type Pipeline struct {
	Cmds   []*SimpleCmd
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

// ListElem is one element of a List: an operator + the pipeline that follows it.
type ListElem struct {
	Op   ListOp
	Node Node // *Pipeline or *Subshell
}

// List is a sequence of pipelines joined by ;  &&  ||  &.
// First holds the leading pipeline; Elems holds subsequent (op, pipeline) pairs.
type List struct {
	First Node       // *Pipeline or *Subshell
	Elems []ListElem // subsequent elements
}

func (*List) nodeTag() {}

// Subshell executes a List in a sub-environment.
type Subshell struct {
	Body *List
}

func (*Subshell) nodeTag() {}
