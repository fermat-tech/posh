package parser

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/fermat-tech/posh/internal/lexer"
)

// ErrIncomplete is returned when the input ends before a compound command is closed.
var ErrIncomplete = errors.New("incomplete input")

// ParseError carries a human-readable parse error message.
type ParseError struct {
	Msg string
}

func (e *ParseError) Error() string { return e.Msg }

// Parser turns a token slice into an AST.
type Parser struct {
	tokens []lexer.Token
	pos    int
	stops  map[string]bool // current reserved-word stop set
}

// New creates a Parser from a pre-tokenized slice.
func New(tokens []lexer.Token) *Parser {
	return &Parser{tokens: tokens, stops: map[string]bool{}}
}

// Parse parses a complete input string and returns a Node (or nil for empty input).
func Parse(input string) (Node, error) {
	return ParseAt(input, 1)
}

func ParseAt(input string, lineBase int) (Node, error) {
	l := lexer.NewAt(input, lineBase)
	toks := l.Tokenize()
	if len(l.Errors) > 0 {
		return nil, &ParseError{Msg: l.Errors[0]}
	}
	p := New(toks)
	return p.parseList()
}

// ---- stop-word helpers ----

func (p *Parser) pushStops(words ...string) map[string]bool {
	old := p.stops
	next := make(map[string]bool, len(old)+len(words))
	for k, v := range old {
		next[k] = v
	}
	for _, w := range words {
		next[w] = true
	}
	p.stops = next
	return old
}

func (p *Parser) popStops(old map[string]bool) {
	p.stops = old
}

func (p *Parser) isStop(t lexer.Token) bool {
	return t.Type == lexer.WORD && p.stops[t.Val]
}

// ---- token helpers ----

// peek returns the next non-newline token without advancing.
func (p *Parser) peek() lexer.Token {
	for pos := p.pos; pos < len(p.tokens); pos++ {
		t := p.tokens[pos]
		if t.Type != lexer.NEWLINE {
			return t
		}
	}
	return lexer.Token{Type: lexer.EOF}
}

// peekRaw returns the next token including newlines.
func (p *Parser) peekRaw() lexer.Token {
	if p.pos < len(p.tokens) {
		return p.tokens[p.pos]
	}
	return lexer.Token{Type: lexer.EOF}
}

// peekNth returns the nth non-newline token (0 = same as peek).
func (p *Parser) peekNth(n int) lexer.Token {
	count := 0
	for pos := p.pos; pos < len(p.tokens); pos++ {
		if p.tokens[pos].Type == lexer.NEWLINE {
			continue
		}
		if count == n {
			return p.tokens[pos]
		}
		count++
	}
	return lexer.Token{Type: lexer.EOF}
}

func (p *Parser) consume() lexer.Token {
	t := p.tokens[p.pos]
	p.pos++
	return t
}

// consumeNonNL advances past any newlines then consumes one token.
func (p *Parser) consumeNonNL() lexer.Token {
	p.skipNewlines()
	return p.consume()
}

func (p *Parser) skipNewlines() {
	for p.pos < len(p.tokens) && p.tokens[p.pos].Type == lexer.NEWLINE {
		p.pos++
	}
}

func (p *Parser) nextWordIs(w string) bool {
	t := p.peek()
	return t.Type == lexer.WORD && t.Val == w
}

func (p *Parser) consumeWord(w string) error {
	p.skipNewlines()
	t := p.peek()
	if t.Type == lexer.EOF {
		return ErrIncomplete
	}
	if t.Type != lexer.WORD || t.Val != w {
		return &ParseError{fmt.Sprintf("expected %q, got %q", w, t.Val)}
	}
	p.consumeNonNL()
	return nil
}

// ---- grammar ----

// isListEnd returns true for tokens that naturally terminate a list.
func (p *Parser) isListEnd(t lexer.Token) bool {
	return t.Type == lexer.EOF || t.Type == lexer.RPAREN || t.Type == lexer.RBRACE || p.isStop(t)
}

// parseList is the top-level list parser; stops at EOF, RPAREN, RBRACE, or stop words.
func (p *Parser) parseList() (Node, error) {
	p.skipNewlines()
	t := p.peek()
	if p.isListEnd(t) {
		return nil, nil
	}

	first, err := p.parseCommandNode()
	if err != nil {
		return nil, err
	}
	if first == nil {
		return nil, nil
	}

	list := &List{First: first}

	for {
		t = p.peekRaw()
		var op ListOp
		switch t.Type {
		case lexer.SEMI:
			p.consume()
			op = OpSemi
		case lexer.AND:
			p.consume()
			op = OpAnd
		case lexer.OR:
			p.consume()
			op = OpOr
		case lexer.AMP:
			p.consume()
			op = OpAmp
		case lexer.NEWLINE:
			p.consume()
			op = OpSemi
		default:
			if len(list.Elems) == 0 {
				return list.First, nil
			}
			return list, nil
		}

		p.skipNewlines()
		t = p.peek()
		if p.isListEnd(t) {
			list.Elems = append(list.Elems, ListElem{Op: op, Node: nil})
			return list, nil
		}

		next, err := p.parseCommandNode()
		if err != nil {
			return nil, err
		}
		if next == nil {
			return list, nil
		}
		list.Elems = append(list.Elems, ListElem{Op: op, Node: next})
	}
}

// parseCommandNode dispatches to compound commands, subshells, group commands, or pipelines.
// It never returns a typed nil — callers can safely compare the result to nil.
func (p *Parser) parseCommandNode() (Node, error) {
	t := p.peek()

	// Natural terminators
	if p.isListEnd(t) {
		return nil, nil
	}

	// Group command { list; }
	if t.Type == lexer.LBRACE {
		node, err := p.parseGroupCmd()
		if err != nil || node == nil {
			return node, err
		}
		return p.maybeWrapInPipeline(node)
	}

	// Subshell ( list )
	if t.Type == lexer.LPAREN {
		node, err := p.parseSubshell()
		if err != nil || node == nil {
			return node, err
		}
		return p.maybeWrapInPipeline(node)
	}

	// Arithmetic command (( expr ))
	if t.Type == lexer.DLARITH {
		tok := p.consume()
		node := &ArithCmd{Expr: tok.Val}
		return p.maybeWrapInPipeline(node)
	}

	// Compound commands and function definitions
	if t.Type == lexer.WORD {
		// Function definition: NAME () { ... }
		t1 := p.peekNth(1)
		t2 := p.peekNth(2)
		if t1.Type == lexer.LPAREN && t2.Type == lexer.RPAREN {
			node, err := p.parseFuncDefShort()
			if err != nil || node == nil {
				return node, err
			}
			return p.maybeWrapInPipeline(node)
		}

		switch t.Val {
		case "if":
			node, err := p.parseIfCmd()
			if err != nil || node == nil {
				return node, err
			}
			return p.maybeWrapInPipeline(node)
		case "for":
			node, err := p.parseForCmd()
			if err != nil || node == nil {
				return node, err
			}
			return p.maybeWrapInPipeline(node)
		case "while":
			node, err := p.parseWhileCmd(false)
			if err != nil || node == nil {
				return node, err
			}
			return p.maybeWrapInPipeline(node)
		case "until":
			node, err := p.parseWhileCmd(true)
			if err != nil || node == nil {
				return node, err
			}
			return p.maybeWrapInPipeline(node)
		case "case":
			node, err := p.parseCaseCmd()
			if err != nil || node == nil {
				return node, err
			}
			return p.maybeWrapInPipeline(node)
		case "function":
			node, err := p.parseFuncDefKeyword()
			if err != nil || node == nil {
				return node, err
			}
			return p.maybeWrapInPipeline(node)
		}
	}

	// Fallthrough to pipeline (handles simple commands and their pipes)
	pipe, err := p.parsePipeline()
	if pipe == nil {
		return nil, err
	}
	return pipe, err
}

// maybeWrapInPipeline wraps node in a Pipeline if the next token is |,
// collecting all pipe stages. Used so compound commands can appear as
// the left-hand side of a pipeline: (env;env;env) | less, { ls } | grep foo, etc.
func (p *Parser) maybeWrapInPipeline(first Node) (Node, error) {
	if p.peek().Type != lexer.PIPE {
		return first, nil
	}
	pipe := &Pipeline{Cmds: []Node{first}}
	for p.peek().Type == lexer.PIPE {
		p.consumeNonNL()
		p.skipNewlines()
		next, err := p.parsePipelineSegment()
		if err != nil {
			return nil, err
		}
		if next == nil {
			return nil, &ParseError{"expected command after '|'"}
		}
		pipe.Cmds = append(pipe.Cmds, next)
	}
	return pipe, nil
}

// ---- compound command parsers ----

func (p *Parser) parseGroupCmd() (*GroupCmd, error) {
	p.consumeNonNL() // consume {
	// No need to pushStops("}"): RBRACE is handled by isListEnd in parseList.
	body, err := p.parseList()
	if err != nil {
		return nil, err
	}
	p.skipNewlines()
	t := p.peek()
	if t.Type == lexer.EOF {
		return nil, ErrIncomplete
	}
	if t.Type != lexer.RBRACE {
		return nil, &ParseError{fmt.Sprintf("expected '}', got %q", t.Val)}
	}
	p.consumeNonNL()

	var bodyList *List
	if body != nil {
		switch v := body.(type) {
		case *List:
			bodyList = v
		default:
			bodyList = &List{First: body}
		}
	}
	cmd := &GroupCmd{Body: bodyList}
	cmd.Redirs = p.parseRedirs()
	return cmd, nil
}

func (p *Parser) parseSubshell() (*Subshell, error) {
	p.consumeNonNL() // (
	body, err := p.parseList()
	if err != nil {
		return nil, err
	}
	p.skipNewlines()
	t := p.peek()
	if t.Type == lexer.EOF {
		return nil, ErrIncomplete
	}
	if t.Type != lexer.RPAREN {
		return nil, &ParseError{fmt.Sprintf("expected ')', got %q", t.Val)}
	}
	p.consumeNonNL()

	var bodyList *List
	if body != nil {
		switch v := body.(type) {
		case *List:
			bodyList = v
		default:
			bodyList = &List{First: body}
		}
	}
	sub := &Subshell{Body: bodyList}
	sub.Redirs = p.parseRedirs()
	return sub, nil
}

func (p *Parser) parseIfCmd() (*IfCmd, error) {
	p.consumeNonNL() // consume 'if'

	// condition
	old := p.pushStops("then", "fi", "elif", "else")
	cond, err := p.parseList()
	p.popStops(old)
	if err != nil {
		return nil, err
	}
	if err := p.consumeWord("then"); err != nil {
		return nil, err
	}

	// then body
	old = p.pushStops("fi", "elif", "else")
	then, err := p.parseList()
	p.popStops(old)
	if err != nil {
		return nil, err
	}

	cmd := &IfCmd{Cond: toList(cond), Then: toList(then)}

	// elif / else / fi
	for {
		p.skipNewlines()
		t := p.peek()
		if t.Type == lexer.EOF {
			return nil, ErrIncomplete
		}
		switch {
		case t.Type == lexer.WORD && t.Val == "elif":
			p.consumeNonNL()
			old = p.pushStops("then", "fi", "elif", "else")
			econd, err := p.parseList()
			p.popStops(old)
			if err != nil {
				return nil, err
			}
			if err := p.consumeWord("then"); err != nil {
				return nil, err
			}
			old = p.pushStops("fi", "elif", "else")
			ebody, err := p.parseList()
			p.popStops(old)
			if err != nil {
				return nil, err
			}
			cmd.Elifs = append(cmd.Elifs, ElifClause{Cond: toList(econd), Then: toList(ebody)})

		case t.Type == lexer.WORD && t.Val == "else":
			p.consumeNonNL()
			old = p.pushStops("fi")
			ebody, err := p.parseList()
			p.popStops(old)
			if err != nil {
				return nil, err
			}
			cmd.Else = toList(ebody)
			if err := p.consumeWord("fi"); err != nil {
				return nil, err
			}
			cmd.Redirs = p.parseRedirs()
			return cmd, nil

		case t.Type == lexer.WORD && t.Val == "fi":
			p.consumeNonNL()
			cmd.Redirs = p.parseRedirs()
			return cmd, nil

		default:
			return nil, &ParseError{fmt.Sprintf("expected 'fi', 'elif', or 'else'; got %q", t.Val)}
		}
	}
}

func (p *Parser) parseForCmd() (*ForCmd, error) {
	p.consumeNonNL() // consume 'for'

	t := p.peek()
	if t.Type != lexer.WORD {
		return nil, &ParseError{"expected variable name after 'for'"}
	}
	varName := t.Val
	p.consumeNonNL()

	cmd := &ForCmd{Var: varName}

	// optional 'in words...'
	p.skipNewlines()
	if p.nextWordIs("in") {
		p.consumeNonNL()
		// collect words until ; or newline or 'do'
		for {
			t = p.peek()
			if t.Type == lexer.EOF || t.Type == lexer.SEMI || t.Type == lexer.NEWLINE ||
				(t.Type == lexer.WORD && t.Val == "do") {
				break
			}
			t = p.consumeNonNL()
			cmd.Words = append(cmd.Words, t.Val)
		}
	}

	// skip ; or newline before 'do'
	for {
		t = p.peekRaw()
		if t.Type == lexer.SEMI || t.Type == lexer.NEWLINE {
			p.consume()
		} else {
			break
		}
	}

	if err := p.consumeWord("do"); err != nil {
		return nil, err
	}

	old := p.pushStops("done")
	body, err := p.parseList()
	p.popStops(old)
	if err != nil {
		return nil, err
	}
	if err := p.consumeWord("done"); err != nil {
		return nil, err
	}

	cmd.Body = toList(body)
	cmd.Redirs = p.parseRedirs()
	return cmd, nil
}

func (p *Parser) parseWhileCmd(until bool) (*WhileCmd, error) {
	p.consumeNonNL() // consume 'while' or 'until'

	old := p.pushStops("do")
	cond, err := p.parseList()
	p.popStops(old)
	if err != nil {
		return nil, err
	}

	if err := p.consumeWord("do"); err != nil {
		return nil, err
	}

	old = p.pushStops("done")
	body, err := p.parseList()
	p.popStops(old)
	if err != nil {
		return nil, err
	}
	if err := p.consumeWord("done"); err != nil {
		return nil, err
	}

	cmd := &WhileCmd{Cond: toList(cond), Body: toList(body), Until: until}
	cmd.Redirs = p.parseRedirs()
	return cmd, nil
}

func (p *Parser) parseCaseCmd() (*CaseCmd, error) {
	p.consumeNonNL() // consume 'case'

	t := p.peek()
	if t.Type == lexer.EOF {
		return nil, ErrIncomplete
	}
	wordTok := p.consumeNonNL()

	if err := p.consumeWord("in"); err != nil {
		return nil, err
	}
	p.skipNewlines()

	cmd := &CaseCmd{Word: wordTok.Val}

	for {
		p.skipNewlines()
		t = p.peek()
		if t.Type == lexer.EOF {
			return nil, ErrIncomplete
		}
		if t.Type == lexer.WORD && t.Val == "esac" {
			p.consumeNonNL()
			break
		}

		// Optional leading (
		if t.Type == lexer.LPAREN {
			p.consumeNonNL()
		}

		// Collect patterns separated by |
		var patterns []string
		for {
			t = p.peek()
			if t.Type == lexer.EOF {
				return nil, ErrIncomplete
			}
			if t.Type == lexer.RPAREN {
				p.consumeNonNL()
				break
			}
			patterns = append(patterns, p.consumeNonNL().Val)
			t = p.peek()
			if t.Type == lexer.PIPE {
				p.consumeNonNL()
			}
		}

		// Parse clause body until ;; or esac
		old := p.pushStops("esac")
		var clauses []Node
		for {
			p.skipNewlines()
			t = p.peek()
			if t.Type == lexer.EOF {
				p.popStops(old)
				return nil, ErrIncomplete
			}
			if t.Type == lexer.WORD && t.Val == "esac" {
				break
			}
			// ;; terminates the clause
			if t.Type == lexer.SEMI {
				// peek next
				p.consume()
				next := p.peekRaw()
				if next.Type == lexer.SEMI {
					p.consume()
					break
				}
				// single ; — continue
				continue
			}
			node, err := p.parseCommandNode()
			if err != nil {
				p.popStops(old)
				return nil, err
			}
			if node != nil {
				clauses = append(clauses, node)
			}
		}
		p.popStops(old)

		var bodyList *List
		if len(clauses) > 0 {
			bodyList = &List{First: clauses[0]}
			for _, c := range clauses[1:] {
				bodyList.Elems = append(bodyList.Elems, ListElem{Op: OpSemi, Node: c})
			}
		}
		cmd.Clauses = append(cmd.Clauses, CaseClause{Patterns: patterns, Body: bodyList})
	}

	cmd.Redirs = p.parseRedirs()
	return cmd, nil
}

func (p *Parser) parseFuncDefShort() (*FuncDef, error) {
	name := p.consumeNonNL().Val // NAME
	p.consumeNonNL()              // (
	p.consumeNonNL()              // )
	p.skipNewlines()

	body, err := p.parseFuncBody()
	if err != nil {
		return nil, err
	}
	return &FuncDef{Name: name, Body: body}, nil
}

func (p *Parser) parseFuncDefKeyword() (*FuncDef, error) {
	p.consumeNonNL() // consume 'function'

	t := p.peek()
	if t.Type != lexer.WORD {
		return nil, &ParseError{"expected function name after 'function'"}
	}
	name := t.Val
	p.consumeNonNL()

	// optional ()
	p.skipNewlines()
	if p.peek().Type == lexer.LPAREN {
		p.consumeNonNL() // (
		if p.peek().Type != lexer.RPAREN {
			return nil, &ParseError{"expected ')' after '('"}
		}
		p.consumeNonNL() // )
	}
	p.skipNewlines()

	body, err := p.parseFuncBody()
	if err != nil {
		return nil, err
	}
	return &FuncDef{Name: name, Body: body}, nil
}

// parseFuncBody parses the function body (a compound command or group).
func (p *Parser) parseFuncBody() (Node, error) {
	t := p.peek()
	switch t.Type {
	case lexer.LBRACE:
		return p.parseGroupCmd()
	case lexer.LPAREN:
		return p.parseSubshell()
	case lexer.WORD:
		switch t.Val {
		case "if":
			return p.parseIfCmd()
		case "for":
			return p.parseForCmd()
		case "while":
			return p.parseWhileCmd(false)
		case "until":
			return p.parseWhileCmd(true)
		case "case":
			return p.parseCaseCmd()
		}
	}
	if t.Type == lexer.EOF {
		return nil, ErrIncomplete
	}
	return nil, &ParseError{fmt.Sprintf("expected function body, got %q", t.Val)}
}

// ---- pipeline parser ----

func (p *Parser) parsePipeline() (*Pipeline, error) {
	negate := false
	if t := p.peek(); t.Type == lexer.WORD && t.Val == "!" {
		p.consumeNonNL()
		negate = true
	}

	first, err := p.parsePipelineSegment()
	if err != nil {
		return nil, err
	}
	if first == nil {
		return nil, nil
	}

	pipe := &Pipeline{Cmds: []Node{first}, Negate: negate}

	for p.peek().Type == lexer.PIPE {
		p.consumeNonNL()
		p.skipNewlines()
		next, err := p.parsePipelineSegment()
		if err != nil {
			return nil, err
		}
		if next == nil {
			return nil, &ParseError{"expected command after '|'"}
		}
		pipe.Cmds = append(pipe.Cmds, next)
	}

	return pipe, nil
}

// parsePipelineSegment parses one segment of a pipeline — a compound command
// (subshell, group, if, for, while, case) or a simple command. It does not
// recurse into parsePipeline, avoiding infinite recursion.
func (p *Parser) parsePipelineSegment() (Node, error) {
	t := p.peek()
	if p.isListEnd(t) {
		return nil, nil
	}
	if t.Type == lexer.LBRACE {
		return p.parseGroupCmd()
	}
	if t.Type == lexer.LPAREN {
		return p.parseSubshell()
	}
	if t.Type == lexer.DLARITH {
		tok := p.consume()
		return &ArithCmd{Expr: tok.Val}, nil
	}
	if t.Type == lexer.WORD {
		t1 := p.peekNth(1)
		t2 := p.peekNth(2)
		if t1.Type == lexer.LPAREN && t2.Type == lexer.RPAREN {
			return p.parseFuncDefShort()
		}
		switch t.Val {
		case "if":
			return p.parseIfCmd()
		case "for":
			return p.parseForCmd()
		case "while":
			return p.parseWhileCmd(false)
		case "until":
			return p.parseWhileCmd(true)
		case "case":
			return p.parseCaseCmd()
		case "function":
			return p.parseFuncDefKeyword()
		}
	}
	cmd, err := p.parseSimpleCmd()
	if cmd == nil {
		return nil, err
	}
	return cmd, err
}

func (p *Parser) parseSimpleCmd() (*SimpleCmd, error) {
	// Bail on stop words
	t := p.peek()
	if t.Type == lexer.WORD && p.stops[t.Val] {
		return nil, nil
	}

	cmd := &SimpleCmd{}

	// Collect leading VAR=val assignments
	for p.peek().Type == lexer.ASSIGN {
		t = p.consume()
		cmd.Assigns = append(cmd.Assigns, t.Val)
	}

	// Collect words and redirections.
	// Use peekRaw() so NEWLINE tokens are visible and terminate the command.
	// (peek() skips newlines and would merge multi-line content into one command.)
	for {
		t = p.peekRaw()
		if t.Type == lexer.NEWLINE {
			goto done
		}
		if t.Type == lexer.WORD && p.stops[t.Val] {
			break
		}
		switch t.Type {
		case lexer.WORD:
			p.consume()
			cmd.Words = append(cmd.Words, t.Val)

		case lexer.REDIR_OUT, lexer.REDIR_APPEND, lexer.REDIR_IN,
			lexer.REDIR_ERR, lexer.REDIR_ERR_APPEND, lexer.REDIR_BOTH, lexer.REDIR_BOTH_APPEND,
			lexer.REDIR_FD_OUT, lexer.REDIR_FD_APPEND, lexer.REDIR_FD_IN:
			op := t.Type
			fd1, _ := strconv.Atoi(t.Val)
			p.consume()
			file := p.peek()
			if file.Type != lexer.WORD {
				return nil, &ParseError{fmt.Sprintf("expected filename after %s", op)}
			}
			p.consume()
			cmd.Redirs = append(cmd.Redirs, Redir{Op: op, File: file.Val, Fd1: fd1})

		case lexer.REDIR_DUP_OUT, lexer.REDIR_DUP_IN:
			r, err := parseDupRedir(t)
			if err != nil {
				return nil, err
			}
			p.consume()
			cmd.Redirs = append(cmd.Redirs, r)

		case lexer.REDIR_CLOSE_OUT, lexer.REDIR_CLOSE_IN:
			fd1, _ := strconv.Atoi(t.Val)
			p.consume()
			cmd.Redirs = append(cmd.Redirs, Redir{Op: t.Type, Fd1: fd1})

		case lexer.HEREDOC_OP, lexer.HEREDOC_STRIP_OP:
			strip := t.Type == lexer.HEREDOC_STRIP_OP
			p.consume()
			delim := p.peek()
			if delim.Type != lexer.WORD {
				return nil, &ParseError{"expected heredoc delimiter"}
			}
			p.consume()
			redir := Redir{Op: lexer.HEREDOC_OP, Delim: delim.Val, Strip: strip}
			// Check for inline body injected by preprocessHeredocs.
			if p.peekRaw().Type == lexer.HEREDOC_BODY {
				body := p.peekRaw()
				p.consumeNonNL()
				redir.File = body.Val
				redir.Expand = !body.Quoted
			}
			cmd.Redirs = append(cmd.Redirs, redir)

		case lexer.HERESTRING_OP:
			p.consume()
			word := p.peekRaw()
			if word.Type != lexer.WORD {
				return nil, &ParseError{"expected word after <<<"}
			}
			p.consumeNonNL()
			cmd.Redirs = append(cmd.Redirs, Redir{Op: lexer.HERESTRING_OP, File: word.Val})

		default:
			goto done
		}
	}

done:
	if len(cmd.Assigns) == 0 && len(cmd.Words) == 0 && len(cmd.Redirs) == 0 {
		return nil, nil
	}
	return cmd, nil
}

// parseRedirs reads any trailing redirections after a compound command.
func (p *Parser) parseRedirs() []Redir {
	var redirs []Redir
	for {
		t := p.peek()
		switch t.Type {
		case lexer.REDIR_OUT, lexer.REDIR_APPEND, lexer.REDIR_IN,
			lexer.REDIR_ERR, lexer.REDIR_ERR_APPEND, lexer.REDIR_BOTH, lexer.REDIR_BOTH_APPEND,
			lexer.REDIR_FD_OUT, lexer.REDIR_FD_APPEND, lexer.REDIR_FD_IN:
			op := t.Type
			fd1, _ := strconv.Atoi(t.Val)
			p.consumeNonNL()
			file := p.peek()
			if file.Type == lexer.WORD {
				p.consumeNonNL()
				redirs = append(redirs, Redir{Op: op, File: file.Val, Fd1: fd1})
			}
		case lexer.REDIR_DUP_OUT, lexer.REDIR_DUP_IN:
			r, err := parseDupRedir(t)
			if err == nil {
				p.consumeNonNL()
				redirs = append(redirs, r)
			}
		case lexer.REDIR_CLOSE_OUT, lexer.REDIR_CLOSE_IN:
			fd1, _ := strconv.Atoi(t.Val)
			p.consumeNonNL()
			redirs = append(redirs, Redir{Op: t.Type, Fd1: fd1})
		case lexer.HEREDOC_OP, lexer.HEREDOC_STRIP_OP:
			strip := t.Type == lexer.HEREDOC_STRIP_OP
			p.consumeNonNL()
			delim := p.peek()
			if delim.Type == lexer.WORD {
				p.consumeNonNL()
				redir := Redir{Op: lexer.HEREDOC_OP, Delim: delim.Val, Strip: strip}
				if p.peek().Type == lexer.HEREDOC_BODY {
					body := p.peek()
					p.consumeNonNL()
					redir.File = body.Val
					redir.Expand = !body.Quoted
				}
				redirs = append(redirs, redir)
			}
		case lexer.HERESTRING_OP:
			p.consumeNonNL()
			word := p.peek()
			if word.Type == lexer.WORD {
				p.consumeNonNL()
				redirs = append(redirs, Redir{Op: lexer.HERESTRING_OP, File: word.Val})
			}
		default:
			return redirs
		}
	}
}

// parseDupRedir parses a REDIR_DUP_OUT or REDIR_DUP_IN token.
// Token Val is "src:dst" e.g. "2:1" for 2>&1.
func parseDupRedir(t lexer.Token) (Redir, error) {
	var src, dst int
	n, err := fmt.Sscanf(t.Val, "%d:%d", &src, &dst)
	if err != nil || n != 2 {
		return Redir{}, &ParseError{fmt.Sprintf("malformed fd-dup token %q", t.Val)}
	}
	return Redir{Op: t.Type, Fd1: src, Fd2: dst}, nil
}

// ---- helpers ----

func toList(n Node) *List {
	if n == nil {
		return nil
	}
	if l, ok := n.(*List); ok {
		return l
	}
	return &List{First: n}
}

// heredocPending returns true if input has a heredoc operator whose body has
// not yet been terminated by the matching delimiter line.
func heredocPending(input string) bool {
	type spec struct {
		delim string
		strip bool
	}
	lines := strings.Split(input, "\n")
	var pending []spec

	for _, line := range lines {
		if len(pending) > 0 {
			s := pending[0]
			check := line
			if s.strip {
				check = strings.TrimLeft(line, "\t")
			}
			if check == s.delim {
				pending = pending[1:]
			}
			continue
		}
		// Scan line for << operators (skip <<< here-strings).
		runes := []rune(line)
		inSQ, inDQ := false, false
		for i := 0; i < len(runes); i++ {
			ch := runes[i]
			if inSQ {
				if ch == '\'' {
					inSQ = false
				}
				continue
			}
			if inDQ {
				if ch == '"' {
					inDQ = false
				}
				continue
			}
			if ch == '#' {
				break
			}
			if ch == '\'' {
				inSQ = true
				continue
			}
			if ch == '"' {
				inDQ = true
				continue
			}
			if ch == '<' && i+1 < len(runes) && runes[i+1] == '<' {
				j := i + 2
				if j < len(runes) && runes[j] == '<' {
					// <<< herestring — skip
					i = j
					continue
				}
				strip := false
				if j < len(runes) && runes[j] == '-' {
					strip = true
					j++
				}
				for j < len(runes) && (runes[j] == ' ' || runes[j] == '\t') {
					j++
				}
				if j >= len(runes) {
					continue
				}
				var delim string
				qch := runes[j]
				if qch == '\'' || qch == '"' {
					j++
					start := j
					for j < len(runes) && runes[j] != qch {
						j++
					}
					delim = string(runes[start:j])
					if j < len(runes) {
						j++
					}
				} else {
					start := j
					for j < len(runes) && runes[j] != ' ' && runes[j] != '\t' {
						j++
					}
					delim = string(runes[start:j])
				}
				if delim != "" {
					pending = append(pending, spec{delim: delim, strip: strip})
				}
				i = j - 1
			}
		}
	}
	return len(pending) > 0
}

// NeedsContinuation reports whether the input string looks incomplete.
// Used by the REPL to decide whether to prompt for more input.
func NeedsContinuation(input string) bool {
	// Trailing operators
	trimmed := strings.TrimRight(input, " \t\r\n")
	if strings.HasSuffix(trimmed, "|") ||
		strings.HasSuffix(trimmed, "&&") ||
		strings.HasSuffix(trimmed, "||") ||
		strings.HasSuffix(trimmed, "\\") {
		return true
	}

	// Count keyword nesting using the lexer
	l := lexer.New(input)
	toks := l.Tokenize()
	if len(l.Errors) > 0 {
		return false // let the caller evaluate the chunk so the error gets reported
	}
	depth := 0
	for _, t := range toks {
		if t.Type != lexer.WORD {
			continue
		}
		switch t.Val {
		case "if", "for", "while", "until", "case":
			depth++
		case "fi", "done", "esac":
			depth--
		}
	}
	if depth > 0 {
		return true
	}

	// Unmatched { or (
	braceDepth := 0
	parenDepth := 0
	for _, t := range toks {
		switch t.Type {
		case lexer.LBRACE:
			braceDepth++
		case lexer.RBRACE:
			braceDepth--
		case lexer.LPAREN:
			parenDepth++
		case lexer.RPAREN:
			parenDepth--
		}
	}
	if braceDepth > 0 || parenDepth > 0 {
		return true
	}

	return heredocPending(input)
}
