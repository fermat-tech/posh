// Package lexer tokenizes posh shell input.
package lexer

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// TokenType identifies the kind of a token.
type TokenType int

const (
	WORD             TokenType = iota // bare word, quoted string, or substitution
	ASSIGN                            // VAR=val (only at command start position)
	PIPE                              // |
	AND                               // &&
	OR                                // ||
	SEMI                              // ;
	AMP                               // &
	REDIR_OUT                         // > or 1>
	REDIR_APPEND                      // >> or 1>>
	REDIR_IN                          // < or 0<
	REDIR_ERR                         // 2>
	REDIR_ERR_APPEND                  // 2>>
	REDIR_BOTH                        // &>
	REDIR_BOTH_APPEND                 // &>>
	REDIR_FD_OUT                      // N>file  (N≠1,2); Val = "N"
	REDIR_FD_APPEND                   // N>>file (N≠1,2); Val = "N"
	REDIR_FD_IN                       // N<file  (N≠0);   Val = "N"
	REDIR_DUP_OUT                     // N>&M;  Val = "N:M"
	REDIR_DUP_IN                      // N<&M;  Val = "N:M"
	REDIR_CLOSE_OUT                   // N>&-;  Val = "N"
	REDIR_CLOSE_IN                    // N<&-;  Val = "N"
	HEREDOC_OP                        // <<
	HEREDOC_STRIP_OP                  // <<-
	HEREDOC_BODY                      // inline heredoc body injected by preprocessHeredocs
	HERESTRING_OP                     // <<<
	LPAREN                            // (
	RPAREN                            // )
	LBRACE                            // {
	RBRACE                            // }
	DLARITH                           // (( arithmetic command ))
	NEWLINE                           // \n
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
	case REDIR_BOTH_APPEND:
		return "&>>"
	case REDIR_FD_OUT:
		return "N>"
	case REDIR_FD_APPEND:
		return "N>>"
	case REDIR_FD_IN:
		return "N<"
	case REDIR_DUP_OUT:
		return "N>&M"
	case REDIR_DUP_IN:
		return "N<&M"
	case REDIR_CLOSE_OUT:
		return "N>&-"
	case REDIR_CLOSE_IN:
		return "N<&-"
	case HEREDOC_OP:
		return "<<"
	case HEREDOC_STRIP_OP:
		return "<<-"
	case HEREDOC_BODY:
		return "HEREDOC_BODY"
	case HERESTRING_OP:
		return "<<<"
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
	Type   TokenType
	Val    string // raw text (for WORD/ASSIGN); empty for punctuation tokens
	Quoted bool   // for HEREDOC_BODY: true = literal body (no expansion)
}

// Lexer holds the tokenizer state.
type Lexer struct {
	input      []rune
	pos        int
	line       int      // current 1-based line number
	Errors     []string // unterminated-string diagnostics
	Incomplete bool     // input ended mid-construct (e.g. an unterminated array literal)

	// UnterminatedQuote is true when the input ran out while a quote was still
	// open. Bash treats this as "wait for more input" at an interactive prompt
	// (PS2), since a quoted string may legitimately span many lines; posh's REPL
	// uses this to decide the same way, rather than reporting an error on the
	// first Enter press. See MidWordQuoteBreak for the one case that stays a
	// hard, immediate error instead.
	UnterminatedQuote bool

	// MidWordQuoteBreak is true when a quote fused onto a preceding bareword
	// (e.g. foo'bar) contained an embedded newline before ever closing. Unlike a
	// plain open quote, this is always a hard error — not something more input
	// resolves — since it usually indicates the user pressed Enter by mistake
	// rather than intending a multi-line quoted value.
	MidWordQuoteBreak bool
}

// New creates a Lexer for the given input string.
func New(input string) *Lexer {
	return NewAt(input, 1)
}

func NewAt(input string, lineBase int) *Lexer {
	// Strip UTF-8 BOM so files saved by Windows Notepad / PowerShell don't break.
	input = strings.TrimPrefix(input, "\xef\xbb\xbf")
	return &Lexer{input: []rune(input), line: lineBase}
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
	if ch == '\n' {
		l.line++
	}
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
				if isAssignment(w) {
					w = l.glueArrayLiteral(w)
					if wordPos {
						// assignment-form word in argument position, e.g. declare's args
						tokens = append(tokens, Token{Type: WORD, Val: w})
					} else {
						tokens = append(tokens, Token{Type: ASSIGN, Val: w})
					}
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
				if n2, _ := l.peek(); n2 == '>' {
					l.advance()
					tokens = append(tokens, Token{Type: REDIR_BOTH_APPEND})
				} else {
					tokens = append(tokens, Token{Type: REDIR_BOTH})
				}
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
			} else if next == '&' {
				// >&N  →  1>&N (dup stdout to fd N)
				// >&-  →  close stdout
				n2, ok2 := l.peekAt(1)
				if ok2 && n2 >= '0' && n2 <= '9' {
					l.advance() // &
					l.advance() // digit
					tokens = append(tokens, Token{Type: REDIR_DUP_OUT, Val: "1:" + string(n2)})
				} else if ok2 && n2 == '-' {
					l.advance() // &
					l.advance() // -
					tokens = append(tokens, Token{Type: REDIR_CLOSE_OUT, Val: "1"})
				} else {
					tokens = append(tokens, Token{Type: REDIR_OUT})
				}
			} else {
				tokens = append(tokens, Token{Type: REDIR_OUT})
			}
			wordPos = false

		case ch == '\x01' || ch == '\x03':
			// Inline heredoc body injected by preprocessHeredocs.
			// \x01 = expanding body, \x03 = literal body.
			quoted := ch == '\x03'
			l.advance()
			var sb strings.Builder
			for {
				c, ok := l.peek()
				if !ok || c == '\x02' {
					if ok {
						l.advance()
					}
					break
				}
				l.advance()
				sb.WriteRune(c)
			}
			tokens = append(tokens, Token{Type: HEREDOC_BODY, Val: sb.String(), Quoted: quoted})

		case ch == '<':
			l.advance()
			next, _ := l.peek()
			if next == '<' {
				l.advance()
				n2, _ := l.peek()
				if n2 == '<' {
					l.advance()
					tokens = append(tokens, Token{Type: HERESTRING_OP})
				} else if n2 == '-' {
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
			// N> N>> N< N>&M N<&M N>&- N<&-  for any single digit N
			if ch >= '0' && ch <= '9' {
				if nxt, ok2 := l.peekAt(1); ok2 && (nxt == '>' || nxt == '<') {
					fd := int(ch - '0')
					l.advance()      // consume digit
					dir := l.advance() // consume > or <

					n2, _ := l.peek()

					if dir == '>' {
						if n2 == '>' { // N>>
							l.advance()
							switch fd {
							case 1:
								tokens = append(tokens, Token{Type: REDIR_APPEND})
							case 2:
								tokens = append(tokens, Token{Type: REDIR_ERR_APPEND})
							default:
								tokens = append(tokens, Token{Type: REDIR_FD_APPEND, Val: strconv.Itoa(fd)})
							}
						} else if n2 == '&' { // N>&M or N>&-
							n3, ok3 := l.peekAt(1)
							if ok3 && n3 == '-' {
								l.advance()
								l.advance()
								tokens = append(tokens, Token{Type: REDIR_CLOSE_OUT, Val: strconv.Itoa(fd)})
							} else if ok3 && n3 >= '0' && n3 <= '9' {
								l.advance()
								l.advance()
								tokens = append(tokens, Token{Type: REDIR_DUP_OUT,
									Val: strconv.Itoa(fd) + ":" + string(n3)})
							} else {
								// N>& with no valid target — treat as plain N>
								tokens = append(tokens, l.fdOutToken(fd))
							}
						} else { // N>file
							tokens = append(tokens, l.fdOutToken(fd))
						}
					} else { // dir == '<'
						if n2 == '&' { // N<&M or N<&-
							n3, ok3 := l.peekAt(1)
							if ok3 && n3 == '-' {
								l.advance()
								l.advance()
								tokens = append(tokens, Token{Type: REDIR_CLOSE_IN, Val: strconv.Itoa(fd)})
							} else if ok3 && n3 >= '0' && n3 <= '9' {
								l.advance()
								l.advance()
								tokens = append(tokens, Token{Type: REDIR_DUP_IN,
									Val: strconv.Itoa(fd) + ":" + string(n3)})
							} else {
								tokens = append(tokens, l.fdInToken(fd))
							}
						} else { // N<file
							tokens = append(tokens, l.fdInToken(fd))
						}
					}
					wordPos = false
					continue
				}
			}

			w := l.readWord()
			if isAssignment(w) {
				w = l.glueArrayLiteral(w)
				if wordPos {
					// assignment-form word in argument position, e.g. declare's args
					tokens = append(tokens, Token{Type: WORD, Val: w})
				} else {
					tokens = append(tokens, Token{Type: ASSIGN, Val: w})
				}
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

// isAssignment reports whether s is a variable assignment prefix. It accepts the
// scalar form NAME=..., the append form NAME+=..., and the array-element forms
// NAME[subscript]=... / NAME[subscript]+=... (the subscript itself is left for
// the evaluator to interpret).
// glueArrayLiteral, given an assignment prefix word ending in '=' (or '+='),
// appends a following ( ... ) array literal to it so the whole thing becomes a
// single ASSIGN token. Returns w unchanged when no '(' follows.
func (l *Lexer) glueArrayLiteral(w string) string {
	if ch, ok := l.peek(); !ok || ch != '(' {
		return w
	}
	l.advance() // consume '('
	return w + l.readArrayLiteral()
}

// readArrayLiteral reads from just after the opening '(' through the matching
// ')', returning "(elem1<sep>elem2...)" where each element is a word (with the
// usual quote sentinels applied) and elements are separated by arrayElemSep.
// Whitespace and newlines between elements are skipped.
func (l *Lexer) readArrayLiteral() string {
	var sb strings.Builder
	sb.WriteByte('(')
	first := true
	for {
		for {
			ch, ok := l.peek()
			if !ok || (ch != ' ' && ch != '\t' && ch != '\r' && ch != '\n') {
				break
			}
			l.advance()
		}
		ch, ok := l.peek()
		if !ok {
			// Reached end of input before the closing ')': the array literal is
			// unterminated, so the caller needs more input to complete it.
			l.Incomplete = true
			break
		}
		if ch == ')' {
			l.advance()
			break
		}
		elem := l.readWord()
		if elem == "" {
			// readWord made no progress (an operator char); skip it so we don't loop.
			l.advance()
			continue
		}
		if !first {
			sb.WriteRune(arrayElemSep)
		}
		sb.WriteString(elem)
		first = false
	}
	sb.WriteByte(')')
	return sb.String()
}

// isAssignment reports whether s is a variable assignment prefix. It accepts the
// scalar form NAME=..., the append form NAME+=..., and the array-element forms
// NAME[subscript]=... / NAME[subscript]+=... (the subscript itself is left for
// the evaluator to interpret).
func isAssignment(s string) bool {
	r := []rune(s)
	n := len(r)
	if n == 0 || (!unicode.IsLetter(r[0]) && r[0] != '_') {
		return false
	}
	i := 1
	for i < n && (unicode.IsLetter(r[i]) || unicode.IsDigit(r[i]) || r[i] == '_') {
		i++
	}
	// optional [subscript]
	if i < n && r[i] == '[' {
		depth := 1
		i++
		for i < n && depth > 0 {
			switch r[i] {
			case '[':
				depth++
			case ']':
				depth--
			}
			i++
		}
		if depth != 0 {
			return false
		}
	}
	// optional + of the += append operator
	if i < n && r[i] == '+' {
		i++
	}
	return i < n && r[i] == '='
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
const protectedSingleQuote rune = 0xE00B // prevents single-quote stripping in expandUnquoted

// arrayElemSep separates elements inside an array-literal assignment token, e.g.
// the value of the ASSIGN token for arr=(a "b c" d). The evaluator splits on it.
const arrayElemSep rune = 0xE020

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
			midWord := l.pos > 0 && (unicode.IsLetter(l.input[l.pos-1]) || unicode.IsDigit(l.input[l.pos-1]))
			l.advance()
			// Protect special characters so the evaluator treats them as literals.
			for _, ch := range l.readSingleQuotedCtx(midWord) {
				switch ch {
				case ' ':
					sb.WriteRune(protectedSpace)
				case '\t':
					sb.WriteRune(protectedTab)
				case '\n':
					// A single-quoted string is 100% literal in bash — even an
					// embedded newline. Left unprotected, it would be treated as
					// an ordinary IFS separator during word-splitting and break
					// the quoted string into multiple arguments (e.g. echo
					// 'line1\nline2' would print as two space-joined args instead
					// of preserving the embedded newline).
					sb.WriteRune(protectedNewline)
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
			midWord := l.pos > 0 && (unicode.IsLetter(l.input[l.pos-1]) || unicode.IsDigit(l.input[l.pos-1]))
			l.advance()
			sb.WriteString(l.readDoubleQuotedCtx(midWord))
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
				l.protectRune(&sb, next)
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

func (l *Lexer) unquoteErr(quote rune, startLine int, preview string) string {
	runes := []rune(preview)
	if len(runes) > 10 {
		runes = runes[:10]
	}
	return fmt.Sprintf("line %d: unterminated %s-quoted string: %c%s",
		startLine, map[rune]string{'"': "double", '\'': "single"}[quote], quote, string(runes))
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
	case '\'':
		sb.WriteRune(protectedSingleQuote)
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
	return l.readSingleQuotedCtx(false)
}

func (l *Lexer) readSingleQuotedCtx(midWord bool) string {
	startLine := l.line
	errSet := false
	var sb strings.Builder
	var preview strings.Builder
	previewLen := 0
	for {
		ch, ok := l.peek()
		if !ok {
			l.Errors = append(l.Errors, l.unquoteErr('\'', startLine, preview.String()))
			l.UnterminatedQuote = true
			break
		}
		if ch == '\'' {
			l.advance()
			break
		}
		if midWord && ch == '\n' && !errSet {
			l.Errors = append(l.Errors, l.unquoteErr('\'', startLine, preview.String()))
			l.MidWordQuoteBreak = true
			errSet = true
		}
		l.advance()
		if previewLen < 10 {
			preview.WriteRune(ch)
			previewLen++
		}
		sb.WriteRune(ch)
	}
	return sb.String()
}

// readDoubleQuoted stores content with leading/trailing " sentinels so the evaluator
// can distinguish double-quoted strings from bare words.
func (l *Lexer) readDoubleQuoted() string {
	return l.readDoubleQuotedCtx(false)
}

func (l *Lexer) readDoubleQuotedCtx(midWord bool) string {
	startLine := l.line
	errSet := false
	var sb strings.Builder
	var preview strings.Builder
	previewLen := 0
	sb.WriteByte('"')
	for {
		ch, ok := l.peek()
		if !ok {
			l.Errors = append(l.Errors, l.unquoteErr('"', startLine, preview.String()))
			l.UnterminatedQuote = true
			break
		}
		if ch == '"' {
			l.advance()
			break
		}
		if midWord && ch == '\n' && !errSet {
			l.Errors = append(l.Errors, l.unquoteErr('"', startLine, preview.String()))
			l.MidWordQuoteBreak = true
			errSet = true
		}
		if previewLen < 10 {
			preview.WriteRune(ch)
			previewLen++
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
			l.Incomplete = true // unterminated $( ... ): needs more input
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
			l.Incomplete = true // unterminated ${ ... } / nested group: needs more input
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

// fdOutToken returns the appropriate output-redirect token for fd N.
// fd 1 maps to REDIR_OUT, fd 2 to REDIR_ERR, anything else to REDIR_FD_OUT.
func (l *Lexer) fdOutToken(fd int) Token {
	switch fd {
	case 1:
		return Token{Type: REDIR_OUT}
	case 2:
		return Token{Type: REDIR_ERR}
	default:
		return Token{Type: REDIR_FD_OUT, Val: strconv.Itoa(fd)}
	}
}

// fdInToken returns the appropriate input-redirect token for fd N.
func (l *Lexer) fdInToken(fd int) Token {
	if fd == 0 {
		return Token{Type: REDIR_IN}
	}
	return Token{Type: REDIR_FD_IN, Val: strconv.Itoa(fd)}
}

func isWordStop(ch rune) bool {
	switch ch {
	case ' ', '\t', '\r', '\n',
		'|', '&', ';', '(', ')', '}',
		'>', '<', '#',
		'\x01', '\x02', '\x03': // heredoc body sentinels
		return true
	}
	return false
}
