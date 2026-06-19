package parser

import (
	"fmt"

	"github.com/fermat-tech/posh/internal/lexer"
)

// ParseError carries a human-readable parse error message.
type ParseError struct {
	Msg string
}

func (e *ParseError) Error() string { return e.Msg }

// Parser turns a token slice into an AST.
type Parser struct {
	tokens []lexer.Token
	pos    int
}

// New creates a Parser from a pre-tokenized slice.
func New(tokens []lexer.Token) *Parser {
	return &Parser{tokens: tokens}
}

// Parse parses a complete input line and returns a *List (or nil for empty input).
func Parse(input string) (Node, error) {
	l := lexer.New(input)
	toks := l.Tokenize()
	p := New(toks)
	return p.parseList()
}

// ---- token helpers ----

func (p *Parser) peek() lexer.Token {
	for p.pos < len(p.tokens) {
		t := p.tokens[p.pos]
		if t.Type == lexer.NEWLINE {
			p.pos++
			continue
		}
		return t
	}
	return lexer.Token{Type: lexer.EOF}
}

func (p *Parser) peekRaw() lexer.Token {
	if p.pos < len(p.tokens) {
		return p.tokens[p.pos]
	}
	return lexer.Token{Type: lexer.EOF}
}

func (p *Parser) consume() lexer.Token {
	t := p.tokens[p.pos]
	p.pos++
	return t
}

func (p *Parser) expect(tt lexer.TokenType) (lexer.Token, error) {
	t := p.peek()
	if t.Type != tt {
		return t, &ParseError{fmt.Sprintf("expected %s, got %s %q", tt, t.Type, t.Val)}
	}
	p.consume()
	return t, nil
}

// skipNewlines advances past NEWLINE tokens.
func (p *Parser) skipNewlines() {
	for p.pos < len(p.tokens) && p.tokens[p.pos].Type == lexer.NEWLINE {
		p.pos++
	}
}

// ---- grammar ----
//
// list     ::= pipeline { (';'|'&&'|'||'|'&') pipeline }
// pipeline ::= ['!'] simple_cmd { '|' simple_cmd }
// simple_cmd ::= { ASSIGN } WORD { WORD | redir }
// redir    ::= ('>'|'>>'|'<'|'2>'|'2>>'|'&>') WORD

func (p *Parser) parseList() (Node, error) {
	p.skipNewlines()
	t := p.peek()
	if t.Type == lexer.EOF {
		return nil, nil
	}

	first, err := p.parsePipelineOrSubshell()
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
			// End of list
			if len(list.Elems) == 0 {
				return list.First, nil // single node, no wrapping needed
			}
			return list, nil
		}

		p.skipNewlines()
		t = p.peek()
		if t.Type == lexer.EOF || t.Type == lexer.RPAREN {
			// Trailing operator — fine (e.g. "cmd &")
			list.Elems = append(list.Elems, ListElem{Op: op, Node: nil})
			return list, nil
		}

		next, err := p.parsePipelineOrSubshell()
		if err != nil {
			return nil, err
		}
		list.Elems = append(list.Elems, ListElem{Op: op, Node: next})
	}
}

func (p *Parser) parsePipelineOrSubshell() (Node, error) {
	t := p.peek()
	if t.Type == lexer.LPAREN {
		return p.parseSubshell()
	}
	return p.parsePipeline()
}

func (p *Parser) parseSubshell() (*Subshell, error) {
	p.consume() // (
	body, err := p.parseList()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(lexer.RPAREN); err != nil {
		return nil, err
	}
	var list *List
	if body != nil {
		switch v := body.(type) {
		case *List:
			list = v
		default:
			list = &List{First: body}
		}
	}
	return &Subshell{Body: list}, nil
}

func (p *Parser) parsePipeline() (*Pipeline, error) {
	negate := false
	if t := p.peek(); t.Type == lexer.WORD && t.Val == "!" {
		p.consume()
		negate = true
	}

	cmd, err := p.parseSimpleCmd()
	if err != nil {
		return nil, err
	}
	if cmd == nil {
		return nil, nil
	}

	pipe := &Pipeline{Cmds: []*SimpleCmd{cmd}, Negate: negate}

	for p.peek().Type == lexer.PIPE {
		p.consume() // |
		p.skipNewlines()
		next, err := p.parseSimpleCmd()
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

func (p *Parser) parseSimpleCmd() (*SimpleCmd, error) {
	cmd := &SimpleCmd{}

	// Collect leading VAR=val assignments
	for p.peek().Type == lexer.ASSIGN {
		t := p.consume()
		cmd.Assigns = append(cmd.Assigns, t.Val)
	}

	// Collect words and redirections
	for {
		t := p.peek()
		switch t.Type {
		case lexer.WORD:
			p.consume()
			cmd.Words = append(cmd.Words, t.Val)

		case lexer.REDIR_OUT, lexer.REDIR_APPEND, lexer.REDIR_IN,
			lexer.REDIR_ERR, lexer.REDIR_ERR_APPEND, lexer.REDIR_BOTH:
			op := t.Type
			p.consume()
			file := p.peek()
			if file.Type != lexer.WORD {
				return nil, &ParseError{fmt.Sprintf("expected filename after %s", op)}
			}
			p.consume()
			cmd.Redirs = append(cmd.Redirs, Redir{Op: op, File: file.Val})

		default:
			goto done
		}
	}

done:
	if len(cmd.Assigns) == 0 && len(cmd.Words) == 0 {
		return nil, nil
	}
	return cmd, nil
}
