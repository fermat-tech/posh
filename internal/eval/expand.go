package eval

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// Sentinel runes matching those inserted by the lexer for single-quoted content.
// They survive word splitting and glob expansion; unprotectWord restores them.
const protectedSpace       rune = 0xE001
const protectedTab         rune = 0xE002
const protectedDollar      rune = 0xE003
const protectedBackslash   rune = 0xE004
const protectedStar        rune = 0xE005
const protectedQuestion    rune = 0xE006
const protectedLBracket    rune = 0xE007
const protectedDoubleQuote rune = 0xE008
const protectedLBrace      rune = 0xE009
const protectedNewline     rune = 0xE00A
const protectedSingleQuote rune = 0xE00B

func unprotectWord(s string) string {
	if !strings.ContainsAny(s, string([]rune{
		protectedSpace, protectedTab, protectedDollar,
		protectedBackslash, protectedStar, protectedQuestion, protectedLBracket,
		protectedDoubleQuote, protectedSingleQuote, protectedLBrace, protectedNewline,
	})) {
		return s
	}
	var sb strings.Builder
	for _, r := range s {
		switch r {
		case protectedSpace:
			sb.WriteByte(' ')
		case protectedTab:
			sb.WriteByte('\t')
		case protectedNewline:
			sb.WriteByte('\n')
		case protectedDollar:
			sb.WriteByte('$')
		case protectedBackslash:
			sb.WriteByte('\\')
		case protectedStar:
			sb.WriteByte('*')
		case protectedQuestion:
			sb.WriteByte('?')
		case protectedLBracket:
			sb.WriteByte('[')
		case protectedDoubleQuote:
			sb.WriteByte('"')
		case protectedSingleQuote:
			sb.WriteByte('\'')
		case protectedLBrace:
			sb.WriteByte('{')
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// expandWords expands a slice of raw word tokens into concrete argument strings.
// Steps: brace → tilde → variable → command-sub → arithmetic → word-split → glob → quote stripping.
func (sh *Shell) expandWords(words []string) []string {
	// Brace expansion first — can turn one word into many.
	var braced []string
	for _, w := range words {
		// Don't brace-expand double-quoted strings.
		if strings.HasPrefix(w, `"`) && strings.HasSuffix(w, `"`) && len(w) >= 2 {
			braced = append(braced, w)
		} else {
			braced = append(braced, braceExpand(w)...)
		}
	}

	var result []string
	for _, w := range braced {
		// Double-quoted strings are not word-split or glob-expanded
		if strings.HasPrefix(w, `"`) && strings.HasSuffix(w, `"`) && len(w) >= 2 {
			result = append(result, unprotectWord(sh.expandWord(w)))
			continue
		}
		expanded := sh.expandWord(w)
		// Word splitting: split on IFS characters if expansion produced spaces/tabs
		parts := sh.wordSplit(expanded)
		for _, part := range parts {
			for _, g := range sh.globExpand(part) {
				result = append(result, unprotectWord(g))
			}
		}
	}
	return result
}

// wordSplit splits a string on IFS characters.
func (sh *Shell) wordSplit(s string) []string {
	ifs := sh.getVar("IFS")
	if ifs == "" {
		ifs = " \t\n"
	}
	// If no IFS characters in s, return as-is
	if !strings.ContainsAny(s, ifs) {
		return []string{s}
	}
	var parts []string
	start := -1
	for i, ch := range s {
		if strings.ContainsRune(ifs, ch) {
			if start >= 0 {
				parts = append(parts, s[start:i])
				start = -1
			}
		} else {
			if start < 0 {
				start = i
			}
		}
	}
	if start >= 0 {
		parts = append(parts, s[start:])
	}
	if len(parts) == 0 {
		return nil
	}
	return parts
}

// expandWord performs tilde, variable, command-substitution, arithmetic expansion
// and quote stripping on a single word token.
func (sh *Shell) expandWord(w string) string {
	// Fast path: no special chars or sentinels
	if !strings.ContainsAny(w, `~$"'\`+string([]rune{
		protectedSpace, protectedTab, protectedDollar,
		protectedBackslash, protectedStar, protectedQuestion, protectedLBracket,
		protectedDoubleQuote, protectedSingleQuote, protectedLBrace, protectedNewline,
	})) {
		return w
	}

	// Handle double-quoted string (stored with leading/trailing " sentinels by lexer).
	// Only treat as a pure double-quoted string if the inner content has no raw "
	// (which would indicate adjacent quoted groups like "a""b" concatenated by the lexer).
	if strings.HasPrefix(w, `"`) && strings.HasSuffix(w, `"`) && len(w) >= 2 {
		inner := w[1 : len(w)-1]
		if !strings.ContainsRune(inner, '"') {
			return sh.expandInsideDoubleQuotes(inner)
		}
		// Fall through to expandUnquoted which handles each "..." segment separately.
	}

	// Handle tilde at start (bare, unquoted)
	if strings.HasPrefix(w, "~/") || w == "~" {
		home := sh.homeDir()
		if w == "~" {
			return home
		}
		return filepath.Join(home, w[2:])
	}

	return sh.expandUnquoted(w)
}

// expandInsideDoubleQuotes expands $VAR and $() inside double quotes.
func (sh *Shell) expandInsideDoubleQuotes(s string) string {
	var sb strings.Builder
	runes := []rune(s)
	i := 0
	for i < len(runes) {
		ch := runes[i]
		if ch == '$' {
			val, n := sh.expandDollar(runes, i)
			sb.WriteString(val)
			i += n
		} else {
			sb.WriteRune(ch)
			i++
		}
	}
	return sb.String()
}

// expandUnquoted expands a bare (unquoted) word.
func (sh *Shell) expandUnquoted(s string) string {
	var sb strings.Builder
	runes := []rune(s)
	i := 0
	for i < len(runes) {
		ch := runes[i]
		switch ch {
		case '$':
			val, n := sh.expandDollar(runes, i)
			sb.WriteString(val)
			i += n
		case '\'':
			// Single-quoted segment — literal until closing '
			i++
			for i < len(runes) && runes[i] != '\'' {
				sb.WriteRune(runes[i])
				i++
			}
			if i < len(runes) {
				i++ // closing '
			}
		case '"':
			// Nested double-quoted segment
			i++
			start := i
			for i < len(runes) && runes[i] != '"' {
				i++
			}
			sb.WriteString(sh.expandInsideDoubleQuotes(string(runes[start:i])))
			if i < len(runes) {
				i++ // closing "
			}
		case '\\':
			i++
			if i < len(runes) {
				sb.WriteRune(runes[i])
				i++
			}
		default:
			sb.WriteRune(ch)
			i++
		}
	}
	return sb.String()
}

// expandDollar processes a $ substitution starting at runes[i].
// Returns the expanded string and the number of runes consumed.
func (sh *Shell) expandDollar(runes []rune, i int) (string, int) {
	if i+1 >= len(runes) {
		return "$", 1
	}
	next := runes[i+1]

	switch next {
	case '(':
		// Command substitution $(...) or arithmetic $((...))
		if i+2 < len(runes) && runes[i+2] == '(' {
			val, n := sh.expandArith(runes, i)
			return val, n
		}
		val, n := sh.expandCmdSub(runes, i)
		return val, n

	case '{':
		val, n := sh.expandBrace(runes, i)
		return val, n

	case '?':
		return strconv.Itoa(sh.lastExit), 2

	case '$':
		return strconv.Itoa(os.Getpid()), 2

	case '0':
		return sh.name, 2

	case '@', '*':
		return strings.Join(sh.posParams, " "), 2

	case '#':
		return strconv.Itoa(len(sh.posParams)), 2

	case '1', '2', '3', '4', '5', '6', '7', '8', '9':
		idx := int(next-'1')
		if idx < len(sh.posParams) {
			return sh.posParams[idx], 2
		}
		return "", 2

	default:
		if unicode.IsLetter(next) || next == '_' {
			end := i + 2
			for end < len(runes) && (unicode.IsLetter(runes[end]) || unicode.IsDigit(runes[end]) || runes[end] == '_') {
				end++
			}
			name := string(runes[i+1 : end])
			return sh.getVar(name), end - i
		}
		return "$", 1
	}
}

// expandBrace handles ${VAR} or ${VAR:-default} etc.
func (sh *Shell) expandBrace(runes []rune, i int) (string, int) {
	// Find closing }
	depth := 0
	j := i + 2 // skip ${
	for j < len(runes) {
		if runes[j] == '{' {
			depth++
		} else if runes[j] == '}' {
			if depth == 0 {
				break
			}
			depth--
		}
		j++
	}
	if j >= len(runes) {
		return "${", 2
	}
	inner := string(runes[i+2 : j])
	consumed := j - i + 1

	// ${VAR:-default}
	if idx := strings.Index(inner, ":-"); idx >= 0 {
		name := inner[:idx]
		def := inner[idx+2:]
		val := sh.getVar(name)
		if val == "" {
			return sh.expandWord(def), consumed
		}
		return val, consumed
	}
	// ${VAR:+alt}
	if idx := strings.Index(inner, ":+"); idx >= 0 {
		name := inner[:idx]
		alt := inner[idx+2:]
		if sh.getVar(name) != "" {
			return sh.expandWord(alt), consumed
		}
		return "", consumed
	}
	// ${#VAR}
	if strings.HasPrefix(inner, "#") {
		return strconv.Itoa(len(sh.getVar(inner[1:]))), consumed
	}
	return sh.getVar(inner), consumed
}

// expandCmdSub runs $(...) and returns its trimmed stdout.
func (sh *Shell) expandCmdSub(runes []rune, i int) (string, int) {
	depth := 0
	j := i + 2 // skip $(
	for j < len(runes) {
		if runes[j] == '(' {
			depth++
		} else if runes[j] == ')' {
			if depth == 0 {
				break
			}
			depth--
		}
		j++
	}
	if j >= len(runes) {
		return "$(", 2
	}
	inner := string(runes[i+2 : j])
	consumed := j - i + 1

	out, err := sh.runCaptured(inner)
	if err != nil {
		return "", consumed
	}
	return strings.TrimRight(out, "\n"), consumed
}

// expandArith evaluates $((expr)).
func (sh *Shell) expandArith(runes []rune, i int) (string, int) {
	// Find closing ))
	j := i + 3 // skip $((
	depth := 0
	for j < len(runes)-1 {
		if runes[j] == '(' {
			depth++
		} else if runes[j] == ')' && runes[j+1] == ')' {
			if depth == 0 {
				break
			}
			depth--
		}
		j++
	}
	if j >= len(runes)-1 {
		return "$((", 3
	}
	expr := string(runes[i+3 : j])
	consumed := j - i + 2 // include ))

	val := evalArith(sh, expr)
	return strconv.FormatInt(val, 10), consumed
}

// globExpand expands glob metacharacters in a single word.
// If no metacharacters or no matches, returns the word unchanged.
func (sh *Shell) globExpand(w string) []string {
	// Don't glob if the word came from a quoted context (no metacharacters survive quoting)
	if !strings.ContainsAny(w, "*?[") {
		return []string{w}
	}
	matches, err := filepath.Glob(w)
	if err != nil || len(matches) == 0 {
		return []string{w}
	}
	return matches
}

func (sh *Shell) homeDir() string {
	if h := sh.getVar("HOME"); h != "" {
		return h
	}
	u, err := user.Current()
	if err != nil {
		return "."
	}
	return u.HomeDir
}

// ---- arithmetic evaluator (simple integer, left-to-right, +−×÷%) ----

func evalArith(sh *Shell, expr string) int64 {
	expr = strings.TrimSpace(expr)
	// Expand $VAR references first
	expr = sh.expandUnquoted(expr)
	// Expand remaining bare variable names (e.g. i in $((i+1)))
	expr = expandBareArithVars(sh, expr)
	val, err := parseArithExpr(expr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "posh: arithmetic: %v\n", err)
		return 0
	}
	return val
}

// expandBareArithVars replaces bare identifier names with their numeric shell variable values.
func expandBareArithVars(sh *Shell, expr string) string {
	var sb strings.Builder
	runes := []rune(expr)
	i := 0
	for i < len(runes) {
		ch := runes[i]
		if unicode.IsLetter(ch) || ch == '_' {
			j := i
			for j < len(runes) && (unicode.IsLetter(runes[j]) || unicode.IsDigit(runes[j]) || runes[j] == '_') {
				j++
			}
			name := string(runes[i:j])
			val := sh.getVar(name)
			if val == "" {
				val = "0"
			}
			sb.WriteString(val)
			i = j
		} else {
			sb.WriteRune(ch)
			i++
		}
	}
	return sb.String()
}

func parseArithExpr(s string) (int64, error) {
	s = strings.TrimSpace(s)
	return parseArithOr(s)
}

// parseArithOr handles || (lowest precedence)
func parseArithOr(s string) (int64, error) {
	depth := 0
	for i := len(s) - 1; i >= 1; i-- {
		switch s[i] {
		case ')':
			depth++
		case '(':
			depth--
		case '|':
			if depth == 0 && s[i-1] == '|' {
				left, err := parseArithOr(s[:i-1])
				if err != nil {
					return 0, err
				}
				right, err := parseArithAnd(s[i+1:])
				if err != nil {
					return 0, err
				}
				if left != 0 || right != 0 {
					return 1, nil
				}
				return 0, nil
			}
		}
	}
	return parseArithAnd(s)
}

// parseArithAnd handles &&
func parseArithAnd(s string) (int64, error) {
	depth := 0
	for i := len(s) - 1; i >= 1; i-- {
		switch s[i] {
		case ')':
			depth++
		case '(':
			depth--
		case '&':
			if depth == 0 && s[i-1] == '&' {
				left, err := parseArithAnd(s[:i-1])
				if err != nil {
					return 0, err
				}
				right, err := parseArithCmp(s[i+1:])
				if err != nil {
					return 0, err
				}
				if left != 0 && right != 0 {
					return 1, nil
				}
				return 0, nil
			}
		}
	}
	return parseArithCmp(s)
}

// parseArithCmp handles ==, !=, <=, >=, <, >
func parseArithCmp(s string) (int64, error) {
	depth := 0
	for i := len(s) - 1; i >= 0; i-- {
		switch s[i] {
		case ')':
			depth++
		case '(':
			depth--
		}
		if depth != 0 {
			continue
		}
		// Two-char operators: check s[i-1:i+1]
		if i > 0 {
			op2 := s[i-1 : i+1]
			switch op2 {
			case "==", "!=", "<=", ">=":
				left, err := parseArithAdd(strings.TrimSpace(s[:i-1]))
				if err != nil {
					return 0, err
				}
				right, err := parseArithAdd(strings.TrimSpace(s[i+1:]))
				if err != nil {
					return 0, err
				}
				switch op2 {
				case "==":
					if left == right {
						return 1, nil
					}
					return 0, nil
				case "!=":
					if left != right {
						return 1, nil
					}
					return 0, nil
				case "<=":
					if left <= right {
						return 1, nil
					}
					return 0, nil
				case ">=":
					if left >= right {
						return 1, nil
					}
					return 0, nil
				}
			}
		}
		// One-char < and > (not part of <= or >=)
		if s[i] == '<' && i > 0 && (i+1 >= len(s) || s[i+1] != '=') {
			left, err := parseArithAdd(strings.TrimSpace(s[:i]))
			if err != nil {
				return 0, err
			}
			right, err := parseArithAdd(strings.TrimSpace(s[i+1:]))
			if err != nil {
				return 0, err
			}
			if left < right {
				return 1, nil
			}
			return 0, nil
		}
		if s[i] == '>' && i > 0 && (i+1 >= len(s) || s[i+1] != '=') {
			left, err := parseArithAdd(strings.TrimSpace(s[:i]))
			if err != nil {
				return 0, err
			}
			right, err := parseArithAdd(strings.TrimSpace(s[i+1:]))
			if err != nil {
				return 0, err
			}
			if left > right {
				return 1, nil
			}
			return 0, nil
		}
	}
	return parseArithAdd(s)
}

func parseArithAdd(s string) (int64, error) {
	// Split on lowest-precedence + and - (respecting parens)
	depth := 0
	for i := len(s) - 1; i >= 0; i-- {
		switch s[i] {
		case ')':
			depth++
		case '(':
			depth--
		case '+', '-':
			if depth == 0 && i > 0 {
				left, err := parseArithAdd(s[:i])
				if err != nil {
					return 0, err
				}
				right, err := parseArithMul(s[i+1:])
				if err != nil {
					return 0, err
				}
				if s[i] == '+' {
					return left + right, nil
				}
				return left - right, nil
			}
		}
	}
	return parseArithMul(s)
}

func parseArithMul(s string) (int64, error) {
	depth := 0
	for i := len(s) - 1; i >= 0; i-- {
		switch s[i] {
		case ')':
			depth++
		case '(':
			depth--
		case '*', '/', '%':
			if depth == 0 && i > 0 {
				left, err := parseArithMul(s[:i])
				if err != nil {
					return 0, err
				}
				right, err := parseArithAtom(s[i+1:])
				if err != nil {
					return 0, err
				}
				switch s[i] {
				case '*':
					return left * right, nil
				case '/':
					if right == 0 {
						return 0, fmt.Errorf("division by zero")
					}
					return left / right, nil
				case '%':
					if right == 0 {
						return 0, fmt.Errorf("division by zero")
					}
					return left % right, nil
				}
			}
		}
	}
	return parseArithAtom(s)
}

func parseArithAtom(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		return parseArithExpr(s[1 : len(s)-1])
	}
	if strings.HasPrefix(s, "!") {
		v, err := parseArithAtom(s[1:])
		if v == 0 {
			return 1, err
		}
		return 0, err
	}
	if strings.HasPrefix(s, "-") {
		v, err := parseArithAtom(s[1:])
		return -v, err
	}
	v, err := strconv.ParseInt(s, 0, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q", s)
	}
	return v, nil
}
