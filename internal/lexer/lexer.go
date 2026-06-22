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
	DLARITH                        // (( arithmetic command ))
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
	case DLARITH:
		return "(("
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
			if next, ok2 := l.peek(); ok2 && next == '(' {
				// (( arithmetic command )) — read until matching ))
				l.advance()
				var sb strings.Builder
				depth := 1
				for {
					c, ok3 := l.peek()
					if !ok3 {
						break
					}
					l.advance()
					if c == '(' {
						depth++
						sb.WriteRune(c)
					} else if c == ')' {
						if depth > 1 {
							depth--
							sb.WriteRune(c)
						} else {
							// closing first ): consume optional second )
							if c2, ok4 := l.peek(); ok4 && c2 == ')' {
								l.advance()
							}
							break
						}
					} else {
						sb.WriteRune(c)
					}
				}
				tokens = append(tokens, Token{Type: DLARITH, Val: strings.TrimSpace(sb.String())})
				wordPos = true
			} else {
				tokens = append(tokens, Token{Type: LPAREN})
				wordPos = false
			}

		case ch == ')':
			l.advance()
			tokens = append(tokens, Token{Type: RPAREN})
			wordPos = false

		case ch == '{':
			// Emit LBRACE only when { is standalone (followed by whitespace/EOF).
			// When { is adjacent to word chars it starts a brace expression and is
			// read as part of the word by readWord.
			next, hasNext := l.peekAt(1)
			standalone := !hasNext || next == ' ' || next == '\t' || next == '\r' || next == '\n'
			if standalone {
				l.advance()
				tokens = append(tokens, Token{Type: LBRACE})
				wordPos = false
			} else {
				w := l.readWord()
				if !wordPos && isAssignment(w) {
					tokens = append(tokens, Token{Type: ASSIGN, Val: w})
				} else {
					tokens = append(tokens, Token{Type: WORD, Val: w})
					wordPos = true
					switch w {
					case "do", "then", "else", "elif":
						wordPos = false
					}
				}
			}

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

// Private Use Area sentinels for characters inside single-quoted strings.
// These survive word splitting and glob expansion unchanged; the evaluator
// converts them back to their real characters after splitting is done.
const protectedSpace       rune = 0xE001
const protectedTab         rune = 0xE002
const protectedDollar      rune = 0xE003 // prevents variable expansion
const protectedBackslash   rune = 0xE004 // prevents escape processing
const protectedStar        rune = 0xE005 // prevents glob expansion
const protectedQuestion    rune = 0xE006 // prevents glob expansion
const protectedLBracket    rune = 0xE007 // prevents glob expansion
const protectedDoubleQuote rune = 0xE008 // prevents double-quote stripping in expandUnquoted
const protectedLBrace      rune = 0xE009 // prevents brace expansion
const protectedNewline     rune = 0xE00A // literal newline from $'...' quoting

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
			// Protect special characters so the evaluator treats them as literals.
			for _, ch := range l.readSingleQuoted() {
				switch ch {
				case ' ':
					sb.WriteRune(protectedSpace)
				case '\t':
					sb.WriteRune(protectedTab)
				case '$':
					sb.WriteRune(protectedDollar)
				case '\\':
					sb.WriteRune(protectedBackslash)
				case '"':
					sb.WriteRune(protectedDoubleQuote)
				case '{':
					sb.WriteRune(protectedLBrace)
				case '*':
					sb.WriteRune(protectedStar)
				case '?':
					sb.WriteRune(protectedQuestion)
				case '[':
					sb.WriteRune(protectedLBracket)
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
		case '{':
			// Brace group mid-word: consume everything up to the matching }.
			l.advance()
			sb.WriteRune('{')
			l.readBraceGroup(&sb)
		case '$':
			l.advance()
			ch2, ok2 := l.peek()
			if ok2 && ch2 == '\'' {
				// ANSI-C quoting: $'...' — escape sequences become literal chars
				l.advance() // consume '
				l.readAnsiCQuoted(&sb)
			} else {
				sb.WriteRune('$')
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
				} else if ch2 == '#' {
					// $# — number of positional params; consume # so it isn't treated as comment
					l.advance()
					sb.WriteRune('#')
				}
				// else: $VAR — variable name will be read as ordinary chars in next iterations
			}
		default:
			l.advance()
			sb.WriteRune(ch)
		}
	}
	return sb.String()
}

// readAnsiCQuoted reads the body of a $'...' string (opening ' already consumed),
// converting ANSI-C escape sequences to their literal character values.
// All characters are protected with sentinels so word-splitting and glob expansion
// treat them as quoted literals.
func (l *Lexer) readAnsiCQuoted(sb *strings.Builder) {
	for {
		ch, ok := l.peek()
		if !ok || ch == '\'' {
			if ok {
				l.advance()
			}
			break
		}
		l.advance()
		if ch != '\\' {
			l.protectRune(sb, ch)
			continue
		}
		// Backslash escape
		next, ok2 := l.peek()
		if !ok2 {
			sb.WriteRune(protectedBackslash)
			break
		}
		l.advance()
		switch next {
		case 'n':
			sb.WriteRune(protectedNewline)
		case 't':
			sb.WriteRune(protectedTab)
		case 'r':
			sb.WriteByte('\r')
		case 'a':
			sb.WriteByte('\a')
		case 'b':
			sb.WriteByte('\b')
		case 'v':
			sb.WriteByte('\v')
		case 'f':
			sb.WriteByte('\f')
		case 'e', 'E':
			sb.WriteByte(0x1B) // ESC
		case '\\':
			sb.WriteRune(protectedBackslash)
		case '\'':
			sb.WriteByte('\'')
		case '"':
			sb.WriteRune(protectedDoubleQuote)
		case ' ':
			sb.WriteRune(protectedSpace)
		case '0', '1', '2', '3', '4', '5', '6', '7':
			// Octal: \0NNN — up to 3 digits
			val := int(next - '0')
			for i := 0; i < 2; i++ {
				d, ok3 := l.peek()
				if !ok3 || d < '0' || d > '7' {
					break
				}
				l.advance()
				val = val*8 + int(d-'0')
			}
			sb.WriteRune(rune(val))
		default:
			sb.WriteRune(protectedBackslash)
			sb.WriteRune(next)
		}
	}
}

// protectRune writes ch with the appropriate sentinel if it needs to be
// protected from word-splitting or glob expansion.
func (l *Lexer) protectRune(sb *strings.Builder, ch rune) {
	switch ch {
	case ' ':
		sb.WriteRune(protectedSpace)
	case '\t':
		sb.WriteRune(protectedTab)
	case '\n':
		sb.WriteRune(protectedNewline)
	case '$':
		sb.WriteRune(protectedDollar)
	case '\\':
		sb.WriteRune(protectedBackslash)
	case '*':
		sb.WriteRune(protectedStar)
	case '?':
		sb.WriteRune(protectedQuestion)
	case '[':
		sb.WriteRune(protectedLBracket)
	case '"':
		sb.WriteRune(protectedDoubleQuote)
	case '{':
		sb.WriteRune(protectedLBrace)
	default:
		sb.WriteRune(ch)
	}
}

// readBraceGroup reads from after the opening { up to and including the matching }.
// Used so that {a,b} and file{1..5}.txt are kept as single word tokens.
func (l *Lexer) readBraceGroup(sb *strings.Builder) {
	depth := 1
	for depth > 0 {
		ch, ok := l.peek()
		if !ok {
			break
		}
		l.advance()
		switch ch {
		case '{':
			depth++
		case '}':
			depth--
		}
		sb.WriteRune(ch)
	}
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
				switch next {
				case '\n': // line continuation — discard
				case '"':
					sb.WriteRune(protectedDoubleQuote)
				case '\\':
					sb.WriteRune(protectedBackslash)
				case '$':
					sb.WriteRune(protectedDollar)
				default: // '`'
					sb.WriteRune(next)
				}
			default:
				sb.WriteByte('\\')
				sb.WriteRune(next)
				l.advance()
			}
		case '$':
			l.advance()
			sb.WriteRune('$')
			ch2, ok2 := l.peek()
			if !ok2 {
				break
			}
			if ch2 == '(' {
				l.advance()
				sb.WriteByte('(')
				l.readNestedParens(&sb)
			} else if ch2 == '{' {
				l.advance()
				sb.WriteByte('{')
				l.readUntilClose('{', '}', &sb)
			} else if ch2 == '#' {
				l.advance()
				sb.WriteRune('#')
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
			// Opening " already written by sb.WriteRune(ch) above.
			// Read raw content until matching " so depth counting isn't confused by
			// parens inside the quoted string. Handle \" escapes faithfully.
			for {
				inner, ok2 := l.peek()
				if !ok2 || inner == '"' {
					if ok2 {
						l.advance()
						sb.WriteRune('"')
					}
					break
				}
				if inner == '\\' {
					l.advance()
					sb.WriteByte('\\')
					if esc, ok3 := l.peek(); ok3 {
						l.advance()
						sb.WriteRune(esc)
					}
				} else {
					l.advance()
					sb.WriteRune(inner)
				}
			}
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
		'|', '&', ';', '(', ')', '}',
		'>', '<', '#':
		return true
	}
	return false
}
