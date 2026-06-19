// Package lexer tokenizes posh shell input.
package lexer

import (
	"fmt"
	"strings"
	"unicode"
)

// TokenType identifies the kind of a token.
type TokenType int

const (
	WORD          TokenType = iota // bare word, quoted string, or substitution
	ASSIGN                         // VAR=val (only at command start position)
	PIPE                           // |
	AND                            // &&
	OR                             // ||
	SEMI                           // ;
	AMP                            // &
	REDIR_OUT                      // >
	REDIR_APPEND                   // >>
	REDIR_IN                       // <
	REDIR_ERR                      // 2>
	REDIR_ERR_APPEND               // 2>>
	REDIR_BOTH                     // &> or 2>&1
	HEREDOC_OP                     // <<
	HEREDOC_STRIP_OP               // <<-
	LPAREN                         // (
	RPAREN                         // )
	LBRACE                         // {
	RBRACE                         // }
	NEWLINE                        // \n
	EOF
)

func (t TokenType) String() string {
	switch t {
	case WORD:
		return "WORD"
	case ASSIGN:
		return "ASSIGN"
	case PIPE:
		return "PIPE"
	case AND:
		return "AND"
	case OR:
		return "OR"
	case SEMI:
		return "SEMI"
	case AMP:
		return "AMP"
	case REDIR_OUT:
		return ">"
	case REDIR_APPEND:
		return ">>"
	case REDIR_IN:
		return "<"
	case REDIR_ERR:
		return "2>"
	case REDIR_ERR_APPEND:
		return "2>>"
	case REDIR_BOTH:
		return "&>"
	case HEREDOC_OP:
		return "<<"
	case HEREDOC_STRIP_OP:
		return "<<-"
	case LPAREN:
		return "("
	case RPAREN:
		return ")"
	case LBRACE:
		return "{"
	case RBRACE:
		return "}"
	case NEWLINE:
		return "NEWLINE"
	case EOF:
		return "EOF"
	}
	return fmt.Sprintf("Token(%d)", int(t))
}

// Token is a single lexical unit.
type Token struct {
	Type TokenType
	Val  string // raw text (for WORD/ASSIGN); empty for punctuation tokens
}

// Lexer holds the tokenizer state.
type Lexer struct {
	input []rune
	pos   int
}

// New creates a Lexer for the given input string.
func New(input string) *Lexer {
	// Strip UTF-8 BOM so files saved by Windows Notepad / PowerShell don't break.
	input = strings.TrimPrefix(input, "\xef\xbb\xbf")
	return &Lexer{input: []rune(input)}
}

func (l *Lexer) peek() (rune, bool) {
	if l.pos >= len(l.input) {
		return 0, false
	}
	return l.input[l.pos], true
}

func (l *Lexer) peekAt(offset int) (rune, bool) {
	i := l.pos + offset
	if i >= len(l.input) {
		return 0, false
	}
	return l.input[i], true
}

func (l *Lexer) advance() rune {
	ch := l.input[l.pos]
	l.pos++
	return ch
}

func (l *Lexer) skipSpaces() {
	for {
		ch, ok := l.peek()
		if !ok {
			break
		}
		if ch == ' ' || ch == '\t' || ch == '\r' {
			l.advance()
		} else {
			break
		}
	}
}

// Tokenize lexes the entire input and returns all tokens including EOF.
func (l *Lexer) Tokenize() []Token {
	var tokens []Token
	wordPos := false // are we inside a command position where VAR= is possible?

	for {
		l.skipSpaces()
		ch, ok := l.peek()
		if !ok {
			tokens = append(tokens, Token{Type: EOF})
			break
		}

		switch {
		case ch == '#':
			// comment — consume to end of line
			for {
				c, ok2 := l.peek()
				if !ok2 || c == '\n' {
					break
				}
				l.advance()
			}

		case ch == '\n':
			l.advance()
			tokens = append(tokens, Token{Type: NEWLINE})
			wordPos = false

		case ch == ';':
			l.advance()
			tokens = append(tokens, Token{Type: SEMI})
			wordPos = false

		case ch == '(':
			l.advance()
			tokens = append(tokens, Token{Type: LPAREN})
			wordPos = false

		case ch == ')':
			l.advance()
			tokens = append(tokens, Token{Type: RPAREN})
			wordPos = false

		case ch == '{':
			l.advance()
			tokens = append(tokens, Token{Type: LBRACE})
			wordPos = false

		case ch == '}':
			l.advance()
			tokens = append(tokens, Token{Type: RBRACE})
			wordPos = false

		case ch == '|':
			l.advance()
			next, _ := l.peek()
			if next == '|' {
				l.advance()
				tokens = append(tokens, Token{Type: OR})
			} else {
				tokens = append(tokens, Token{Type: PIPE})
			}
			wordPos = false

		case ch == '&':
			l.advance()
			next, _ := l.peek()
			if next == '&' {
				l.advance()
				tokens = append(tokens, Token{Type: AND})
			} else if next == '>' {
				l.advance()
				tokens = append(tokens, Token{Type: REDIR_BOTH})
			} else {
				tokens = append(tokens, Token{Type: AMP})
			}
			wordPos = false

		case ch == '>':
			l.advance()
			next, _ := l.peek()
			if next == '>' {
				l.advance()
				tokens = append(tokens, Token{Type: REDIR_APPEND})
			} else {
				tokens = append(tokens, Token{Type: REDIR_OUT})
			}
			wordPos = false

		case ch == '<':
			l.advance()
			next, _ := l.peek()
			if next == '<' {
				l.advance()
				n2, _ := l.peek()
				if n2 == '-' {
					l.advance()
					tokens = append(tokens, Token{Type: HEREDOC_STRIP_OP})
				} else {
					tokens = append(tokens, Token{Type: HEREDOC_OP})
				}
			} else {
				tokens = append(tokens, Token{Type: REDIR_IN})
			}
			wordPos = false

		default:
			// Check for 2> and 2>>
			if ch == '2' {
				next, _ := l.peekAt(1)
				if next == '>' {
					l.advance() // consume '2'
					l.advance() // consume '>'
					n2, _ := l.peek()
					if n2 == '>' {
						l.advance()
						tokens = append(tokens, Token{Type: REDIR_ERR_APPEND})
						wordPos = false
						continue
					}
					if n2 == '&' {
						n3, _ := l.peekAt(1)
						if n3 == '1' {
							l.advance() // &
							l.advance() // 1
							tokens = append(tokens, Token{Type: REDIR_BOTH})
							wordPos = false
							continue
						}
					}
					tokens = append(tokens, Token{Type: REDIR_ERR})
					wordPos = false
					continue
				}
			}

			w := l.readWord()
			if !wordPos && isAssignment(w) {
				tokens = append(tokens, Token{Type: ASSIGN, Val: w})
			} else {
				tokens = append(tokens, Token{Type: WORD, Val: w})
				wordPos = true
				// Keywords that open a new command context reset the assignment position.
				switch w {
				case "do", "then", "else", "elif":
					wordPos = false
				}
			}
		}
	}
	return tokens
}

func isAssignment(s string) bool {
	idx := strings.IndexByte(s, '=')
	if idx <= 0 {
		return false
	}
	name := s[:idx]
	for i, ch := range name {
		if i == 0 && !unicode.IsLetter(ch) && ch != '_' {
			return false
		}
		if i > 0 && !unicode.IsLetter(ch) && !unicode.IsDigit(ch) && ch != '_' {
			return false
		}
	}
	return true
}

// protectedSpace / protectedTab are Private Use Area sentinels placed around
// whitespace inside single-quoted strings so word splitting in the evaluator
// doesn't break them apart. The evaluator strips them after splitting.
const protectedSpace rune = 0xE001
const protectedTab   rune = 0xE002

func (l *Lexer) readWord() string {
	var sb strings.Builder
	for {
		ch, ok := l.peek()
		if !ok {
			break
		}
		if isWordStop(ch) {
			break
		}
		switch ch {
		case '\'':
			l.advance()
			// Protect whitespace so word splitting in the evaluator leaves it intact.
			for _, ch := range l.readSingleQuoted() {
				switch ch {
				case ' ':
					sb.WriteRune(protectedSpace)
				case '\t':
					sb.WriteRune(protectedTab)
				default:
					sb.WriteRune(ch)
				}
			}
		case '"':
			l.advance()
			sb.WriteString(l.readDoubleQuoted())
		case '\\':
			l.advance()
			next, ok2 := l.peek()
			if !ok2 {
				sb.WriteByte('\\')
				break
			}
			if next == '\n' {
				l.advance() // line continuation
			} else {
				l.advance()
				switch next {
				case ' ':
					sb.WriteRune(protectedSpace)
				case '\t':
					sb.WriteRune(protectedTab)
				default:
					sb.WriteRune(next)
				}
			}
		case '$':
			l.advance()
			sb.WriteRune('$')
			ch2, ok2 := l.peek()
			if !ok2 {
				break
			}
			if ch2 == '(' {
				// $(...) command substitution or $((...)) arithmetic — read to matching )
				l.advance()
				sb.WriteByte('(')
				l.readNestedParens(&sb)
			} else if ch2 == '{' {
				// ${...} variable substitution — read to matching }
				l.advance()
				sb.WriteByte('{')
				l.readUntilClose('{', '}', &sb)
			}
			// else: $VAR — variable name will be read as ordinary chars in next iterations
		default:
			l.advance()
			sb.WriteRune(ch)
		}
	}
	return sb.String()
}

func (l *Lexer) readSingleQuoted() string {
	var sb strings.Builder
	for {
		ch, ok := l.peek()
		if !ok || ch == '\'' {
			if ok {
				l.advance()
			}
			break
		}
		l.advance()
		sb.WriteRune(ch)
	}
	return sb.String()
}

// readDoubleQuoted stores content with leading/trailing " sentinels so the evaluator
// can distinguish double-quoted strings from bare words.
func (l *Lexer) readDoubleQuoted() string {
	var sb strings.Builder
	sb.WriteByte('"')
	for {
		ch, ok := l.peek()
		if !ok || ch == '"' {
			if ok {
				l.advance()
			}
			break
		}
		switch ch {
		case '\\':
			l.advance()
			next, ok2 := l.peek()
			if !ok2 {
				sb.WriteByte('\\')
				break
			}
			switch next {
			case '"', '\\', '$', '`', '\n':
				l.advance()
				if next != '\n' {
					sb.WriteRune(next)
				}
			default:
				sb.WriteByte('\\')
				sb.WriteRune(next)
				l.advance()
			}
		default:
			l.advance()
			sb.WriteRune(ch)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// readNestedParens reads everything up to the matching ) including nested (...).
// The opening ( has already been consumed and written; this writes the rest through ).
func (l *Lexer) readNestedParens(sb *strings.Builder) {
	depth := 1
	for depth > 0 {
		ch, ok := l.peek()
		if !ok {
			break
		}
		l.advance()
		sb.WriteRune(ch)
		if ch == '(' {
			depth++
		} else if ch == ')' {
			depth--
		} else if ch == '\'' {
			// single-quoted inside $() — read literally
			sb.WriteString(l.readSingleQuoted())
			sb.WriteByte('\'')
		} else if ch == '"' {
			dq := l.readDoubleQuoted()
			sb.WriteString(dq[:len(dq)-1]) // strip trailing sentinel, re-add below
			sb.WriteByte('"')
		}
	}
}

// readUntilClose reads from the current position to the matching close rune.
// The opening rune has already been consumed; this writes content through the closing rune.
func (l *Lexer) readUntilClose(open, close rune, sb *strings.Builder) {
	depth := 1
	for depth > 0 {
		ch, ok := l.peek()
		if !ok {
			break
		}
		l.advance()
		sb.WriteRune(ch)
		if ch == open {
			depth++
		} else if ch == close {
			depth--
		}
	}
}

func isWordStop(ch rune) bool {
	switch ch {
	case ' ', '\t', '\r', '\n',
		'|', '&', ';', '(', ')', '{', '}',
		'>', '<', '#':
		return true
	}
	return false
}
