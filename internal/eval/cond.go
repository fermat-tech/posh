package eval

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/fermat-tech/posh/internal/parser"
	"github.com/mattn/go-isatty"
)

// isTerminalFD implements -t fd: true if the given standard file descriptor is
// connected to a terminal. Only 0/1/2 are checked against the real OS
// stdin/stdout/stderr -- posh does not track an arbitrary fd table, so any
// other number is always false.
func isTerminalFD(fd int) bool {
	var f *os.File
	switch fd {
	case 0:
		f = os.Stdin
	case 1:
		f = os.Stdout
	case 2:
		f = os.Stderr
	default:
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

// evalCondCmd evaluates a [[ expression ]] conditional command. Exit status is
// 0 (true) or 1 (false), matching bash; a malformed expression at evaluation
// time (currently only an invalid =~ regular expression) reports 2, also
// matching bash.
func (sh *Shell) evalCondCmd(cmd *parser.CondCmd) int {
	rIn, rOut, rErr, cleanup, err := applyRedirs(sh, cmd.Redirs, sh.Stdin, sh.Stdout, sh.Stderr)
	if err != nil {
		fmt.Fprintf(sh.Stderr, "%s: %v\n", sh.name, err)
		return 1
	}
	defer cleanup()
	old := [3]interface{}{sh.Stdin, sh.Stdout, sh.Stderr}
	sh.Stdin, sh.Stdout, sh.Stderr = rIn, rOut, rErr
	defer func() { sh.Stdin, sh.Stdout, sh.Stderr = old[0].(io.Reader), old[1].(io.Writer), old[2].(io.Writer) }()

	ok, err := sh.evalCondNode(cmd.Expr)
	if err != nil {
		fmt.Fprintf(sh.Stderr, "%s: %v\n", sh.name, err)
		return 2
	}
	if ok {
		return 0
	}
	return 1
}

// evalCondNode walks a [[ ]] expression tree. && and || short-circuit, same
// as everywhere else in the shell.
func (sh *Shell) evalCondNode(n parser.CondNode) (bool, error) {
	switch v := n.(type) {
	case *parser.CondOr:
		l, err := sh.evalCondNode(v.L)
		if err != nil || l {
			return l, err
		}
		return sh.evalCondNode(v.R)
	case *parser.CondAnd:
		l, err := sh.evalCondNode(v.L)
		if err != nil || !l {
			return l, err
		}
		return sh.evalCondNode(v.R)
	case *parser.CondNot:
		x, err := sh.evalCondNode(v.X)
		if err != nil {
			return false, err
		}
		return !x, nil
	case *parser.CondGroup:
		return sh.evalCondNode(v.X)
	case *parser.CondTest:
		return sh.evalCondTest(v)
	}
	return false, fmt.Errorf("unknown conditional expression node %T", n)
}

// expandCondWord expands a single [[ ]] operand -- parameters, command
// substitution, arithmetic, tilde -- but performs no word-splitting or
// pathname expansion, matching bash's [[ ]] rule that operands are not
// subject to either. Quote-protection sentinels are fully removed; contrast
// with the == / != path, which needs to keep them to tell a quoted glob
// character from an unquoted one.
func (sh *Shell) expandCondWord(w string) string {
	return unprotectWord(sh.expandWord(w))
}

// expandCondPattern expands a [[ ]] ==/!= operand for glob-pattern matching
// (see matchCasePattern), keeping quote-protection sentinels on any glob
// metacharacter (*, ?, [) that was actually quoted -- so e.g. [[ $a ==
// foo"*"bar ]] treats only the middle * as literal, while an unquoted *
// elsewhere in the same word still matches like a glob.
//
// expandWord already does this correctly for a mid-word quoted segment (see
// protectFromSplitting in expand.go), but its whole-word-double-quoted fast
// path does not: that path exists for ordinary callers that check "is this
// entire word quoted" structurally instead (see expandWords) and so strips
// the quotes and returns fully expanded text with no character-level
// protection at all. [[ == ]] needs the character-level version even for a
// whole-word-quoted pattern, so that path is reproduced here with
// protectFromSplitting applied. A whole-word single-quoted pattern needs no
// special handling here: the lexer already protects it at tokenization time.
func (sh *Shell) expandCondPattern(w string) string {
	if strings.HasPrefix(w, `"`) && strings.HasSuffix(w, `"`) && len(w) >= 2 {
		inner := w[1 : len(w)-1]
		if !hasBareDoubleQuote(inner) {
			return protectFromSplitting(sh.expandInsideDoubleQuotes(inner))
		}
	}
	return sh.expandWord(w)
}

func (sh *Shell) evalCondTest(t *parser.CondTest) (bool, error) {
	switch len(t.Args) {
	case 1:
		if t.Op == "" {
			return sh.expandCondWord(t.Args[0]) != "", nil
		}
		return sh.evalCondUnary(t.Op, t.Args[0])
	case 2:
		return sh.evalCondBinary(t.Args[0], t.Op, t.Args[1])
	}
	return false, fmt.Errorf("invalid conditional test")
}

func (sh *Shell) evalCondUnary(op, rawArg string) (bool, error) {
	// -v and -o test names, not expanded values (bash: -v checks whether a
	// *variable* is set, not what some expansion of its name equals).
	if op == "-v" {
		return sh.isVarSet(sh.expandCondWord(rawArg)), nil
	}
	if op == "-o" {
		return sh.GetOpt(sh.expandCondWord(rawArg)), nil
	}

	arg := sh.expandCondWord(rawArg)
	switch op {
	case "-z":
		return len(arg) == 0, nil
	case "-n":
		return len(arg) != 0, nil
	case "-a", "-e":
		_, err := os.Stat(arg)
		return err == nil, nil
	case "-f":
		fi, err := os.Stat(arg)
		return err == nil && fi.Mode().IsRegular(), nil
	case "-d":
		fi, err := os.Stat(arg)
		return err == nil && fi.IsDir(), nil
	case "-r":
		f, err := os.OpenFile(arg, os.O_RDONLY, 0)
		if err != nil {
			return false, nil
		}
		f.Close()
		return true, nil
	case "-w":
		f, err := os.OpenFile(arg, os.O_WRONLY, 0)
		if err != nil {
			return false, nil
		}
		f.Close()
		return true, nil
	case "-x":
		// Windows has no exec permission bit; matches test/[ 's own -x, which
		// treats any non-directory file as "executable".
		fi, err := os.Stat(arg)
		return err == nil && !fi.IsDir(), nil
	case "-s":
		fi, err := os.Stat(arg)
		return err == nil && fi.Size() > 0, nil
	case "-L", "-h":
		fi, err := os.Lstat(arg)
		return err == nil && fi.Mode()&os.ModeSymlink != 0, nil
	case "-p":
		fi, err := os.Stat(arg)
		return err == nil && fi.Mode()&os.ModeNamedPipe != 0, nil
	case "-S":
		fi, err := os.Stat(arg)
		return err == nil && fi.Mode()&os.ModeSocket != 0, nil
	case "-b":
		fi, err := os.Stat(arg)
		return err == nil && fi.Mode()&os.ModeDevice != 0 && fi.Mode()&os.ModeCharDevice == 0, nil
	case "-c":
		fi, err := os.Stat(arg)
		return err == nil && fi.Mode()&os.ModeCharDevice != 0, nil
	case "-g", "-u", "-k":
		// setgid/setuid/sticky bits have no Windows equivalent and Go's
		// os.FileMode does not expose them portably even on Unix.
		return false, nil
	case "-G", "-O":
		// Owned by the effective group/user id: Windows has no POSIX uid/gid
		// to check against.
		return false, nil
	case "-N":
		// Modified since last read: Go's os.FileInfo has no portable atime.
		return false, nil
	case "-t":
		fd, err := strconv.Atoi(arg)
		if err != nil {
			return false, nil
		}
		return isTerminalFD(fd), nil
	}
	return false, fmt.Errorf("%s: unknown unary conditional operator", op)
}

func (sh *Shell) evalCondBinary(lhsRaw, op, rhsRaw string) (bool, error) {
	switch op {
	case "=", "==":
		// Only the RHS is ever a pattern in bash; the LHS is always a plain
		// string regardless of quoting.
		return matchCondPattern(sh.expandCondPattern(rhsRaw), sh.expandCondWord(lhsRaw)), nil
	case "!=":
		return !matchCondPattern(sh.expandCondPattern(rhsRaw), sh.expandCondWord(lhsRaw)), nil
	case "<":
		return sh.expandCondWord(lhsRaw) < sh.expandCondWord(rhsRaw), nil
	case ">":
		return sh.expandCondWord(lhsRaw) > sh.expandCondWord(rhsRaw), nil
	case "=~":
		return sh.evalCondRegex(sh.expandCondWord(lhsRaw), sh.expandCondWord(rhsRaw))
	case "-eq":
		return evalArith(sh, lhsRaw) == evalArith(sh, rhsRaw), nil
	case "-ne":
		return evalArith(sh, lhsRaw) != evalArith(sh, rhsRaw), nil
	case "-lt":
		return evalArith(sh, lhsRaw) < evalArith(sh, rhsRaw), nil
	case "-le":
		return evalArith(sh, lhsRaw) <= evalArith(sh, rhsRaw), nil
	case "-gt":
		return evalArith(sh, lhsRaw) > evalArith(sh, rhsRaw), nil
	case "-ge":
		return evalArith(sh, lhsRaw) >= evalArith(sh, rhsRaw), nil
	case "-nt":
		return fileNewerThan(sh.expandCondWord(lhsRaw), sh.expandCondWord(rhsRaw)), nil
	case "-ot":
		return fileNewerThan(sh.expandCondWord(rhsRaw), sh.expandCondWord(lhsRaw)), nil
	case "-ef":
		return sameFile(sh.expandCondWord(lhsRaw), sh.expandCondWord(rhsRaw)), nil
	}
	return false, fmt.Errorf("%s: unknown binary conditional operator", op)
}

// matchCondPattern is a glob matcher aware of posh's quote-protection
// sentinels (see expandCondPattern): an unquoted *, ?, or [...] in pattern is
// a wildcard exactly like case/filepath.Match, but a QUOTED one -- surviving
// as its protected sentinel rune -- matches that literal character instead,
// even mixed within the same pattern as unquoted wildcards (foo"*"bar matches
// the literal string foo*bar, not e.g. fooXbar). filepath.Match can't express
// this: its pattern syntax has no way to escape * within a single flat
// string, so quoting couldn't disable a wildcard that sits next to one that
// should stay active.
//
// s (the string being tested) is assumed already fully unprotected (plain
// text) -- only pattern may still carry sentinels.
func matchCondPattern(pattern, s string) bool {
	if pattern == "*" {
		return true
	}
	return globMatch([]rune(pattern), []rune(s))
}

func globMatch(pat, s []rune) bool {
	for len(pat) > 0 {
		switch pat[0] {
		case '*':
			// Trailing * matches anything remaining; otherwise try every
			// split point (simple backtracking -- patterns in shell scripts
			// are short enough that this is not a performance concern).
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if globMatch(pat[1:], s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
			pat, s = pat[1:], s[1:]
		case '[':
			end, neg, ok := findClassEnd(pat)
			if !ok {
				// No matching ] -- bash/fnmatch treat a malformed class as a
				// literal '['.
				if len(s) == 0 || s[0] != '[' {
					return false
				}
				pat, s = pat[1:], s[1:]
				continue
			}
			if len(s) == 0 || !runeInClass(pat, neg, end, s[0]) {
				return false
			}
			pat, s = pat[end+1:], s[1:]
		default:
			if len(s) == 0 || s[0] != unprotectRune(pat[0]) {
				return false
			}
			pat, s = pat[1:], s[1:]
		}
	}
	return len(s) == 0
}

// findClassEnd locates the ] closing a [...] character class starting at
// pat[0] == '['. neg reports a leading ! or ^. ok is false if there is no
// closing ] at all (a malformed/unterminated class).
func findClassEnd(pat []rune) (end int, neg bool, ok bool) {
	start := 1
	if start < len(pat) && (pat[start] == '!' || pat[start] == '^') {
		neg = true
		start++
	}
	for i := start; i < len(pat); i++ {
		if pat[i] == ']' {
			return i, neg, true
		}
	}
	return 0, false, false
}

// runeInClass reports whether r matches the [...] class in pat[0:end+1],
// supporting literal members and a-z style ranges.
func runeInClass(pat []rune, neg bool, end int, r rune) bool {
	start := 1
	if neg {
		start = 2
	}
	matched := false
	for i := start; i < end; i++ {
		if i+2 < end && pat[i+1] == '-' {
			if r >= pat[i] && r <= pat[i+2] {
				matched = true
			}
			i += 2
		} else if unprotectRune(pat[i]) == r {
			matched = true
		}
	}
	if neg {
		return !matched
	}
	return matched
}

// unprotectRune maps a single quote-protection sentinel back to its literal
// character; any other rune (including an already-plain one) is unchanged.
func unprotectRune(r rune) rune {
	switch r {
	case protectedSpace:
		return ' '
	case protectedTab:
		return '\t'
	case protectedDollar:
		return '$'
	case protectedBackslash:
		return '\\'
	case protectedStar:
		return '*'
	case protectedQuestion:
		return '?'
	case protectedLBracket:
		return '['
	case protectedDoubleQuote:
		return '"'
	case protectedSingleQuote:
		return '\''
	case protectedLBrace:
		return '{'
	case protectedNewline:
		return '\n'
	default:
		return r
	}
}

// evalCondRegex implements =~: an extended regular expression match. On a
// successful match, POSH_REMATCH (posh's name for bash's BASH_REMATCH) is set
// to the whole match followed by each parenthesized capture group; on a
// failed match, it is cleared. Go's regexp package (RE2) covers the common
// POSIX ERE constructs -- character classes, anchors, alternation, quantifiers
// including {n,m}, and POSIX bracket classes like [[:alpha:]] -- but does not
// support backreferences, which POSIX ERE itself does not define either.
func (sh *Shell) evalCondRegex(s, pattern string) (bool, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, fmt.Errorf("=~: invalid regular expression: %v", err)
	}
	m := re.FindStringSubmatch(s)
	if m == nil {
		delete(sh.arrays, "POSH_REMATCH")
		return false, nil
	}
	sh.arrays["POSH_REMATCH"] = m
	return true, nil
}

// isVarSet implements -v: true if name (or, for name[sub], a specific array
// element) is a currently-set variable -- unlike -n, this is about existence,
// not whether the value happens to be non-empty. posh's dynamic variables
// (RANDOM, SECONDS, POSHPID; see DynamicVarNames) are always considered set,
// matching bash treating its own built-in variables the same way even though
// neither actually stores a value for them.
func (sh *Shell) isVarSet(name string) bool {
	if lb := strings.IndexByte(name, '['); lb > 0 && strings.HasSuffix(name, "]") {
		base := name[:lb]
		sub := name[lb+1 : len(name)-1]
		if sh.isAssoc(base) {
			_, ok := sh.assoc[base][unprotectWord(sh.expandWord(sub))]
			return ok
		}
		if arr, ok := sh.arrays[base]; ok {
			idx := sh.arrayIndex(base, sub)
			return idx >= 0 && idx < len(arr)
		}
		return false
	}
	for _, d := range DynamicVarNames {
		if d == name {
			return true
		}
	}
	if _, ok := sh.vars[name]; ok {
		return true
	}
	if _, ok := sh.arrays[name]; ok {
		return true
	}
	if _, ok := sh.assoc[name]; ok {
		return true
	}
	return false
}

// fileNewerThan implements -nt (and, with arguments swapped, -ot): true if a
// is newer than b, or a exists and b does not.
func fileNewerThan(a, b string) bool {
	fa, erra := os.Stat(a)
	fb, errb := os.Stat(b)
	if erra != nil {
		return false
	}
	if errb != nil {
		return true
	}
	return fa.ModTime().After(fb.ModTime())
}

// sameFile implements -ef: true if a and b refer to the same underlying file.
func sameFile(a, b string) bool {
	fa, erra := os.Stat(a)
	fb, errb := os.Stat(b)
	if erra != nil || errb != nil {
		return false
	}
	return os.SameFile(fa, fb)
}
