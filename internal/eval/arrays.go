package eval

import (
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// joinElems joins array elements for the [@] or [*] forms: [@] uses the field
// separator so each element becomes its own word (even inside double quotes),
// while [*] joins into a single space-separated field.
func joinElems(elems []string, sub string) string {
	if sub == "@" {
		return strings.Join(elems, string(arrayFieldSep))
	}
	return strings.Join(elems, " ")
}

// sortedKeys returns the keys of m in sorted order, so ${assoc[@]} and
// ${!assoc[@]} have a stable, predictable ordering (Go map order is random).
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// builtinDeclare implements `declare` (and its alias `typeset`) for the array
// flags: -A creates associative arrays, -a creates indexed arrays. Each name may
// be a bare NAME or an inline NAME=(...) initializer. Other flags are accepted
// and ignored so common scripts don't error.
func builtinDeclare(sh *Shell, args []string, _ io.Reader, _, stderr io.Writer) int {
	assoc, indexed := false, false
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") && args[i] != "-" {
		for _, f := range args[i][1:] {
			switch f {
			case 'A':
				assoc = true
			case 'a':
				indexed = true
			}
		}
		i++
	}
	for _, arg := range args[i:] {
		name := arg
		if eq := strings.IndexAny(arg, "[=+"); eq >= 0 {
			if ap, ok := parseAssignParts(arg); ok {
				name = ap.name
			}
		}
		if assoc {
			if _, ok := sh.assoc[name]; !ok {
				sh.assoc[name] = make(map[string]string)
			}
		} else if indexed {
			if _, ok := sh.arrays[name]; !ok {
				sh.arrays[name] = nil
			}
		}
		// Apply an inline initializer once the type is registered.
		if strings.ContainsRune(arg, '=') {
			sh.applyAssign(arg)
		}
	}
	return 0
}

// arrayElemSep matches the lexer's separator (lexer.arrayElemSep) used inside an
// array-literal assignment token, e.g. the value of arr=(a "b c" d).
const arrayElemSep rune = 0xE020

// assignParts is a decomposed assignment: NAME, an optional [subscript], whether
// it uses the += append operator, and the raw (unexpanded) right-hand side.
type assignParts struct {
	name   string
	sub    string // subscript text inside [ ]; "" when there is none
	hasSub bool
	append bool
	value  string
}

// parseAssignParts splits an assignment word into its components. It mirrors the
// forms isAssignment accepts: NAME=, NAME+=, NAME[sub]=, NAME[sub]+=.
func parseAssignParts(s string) (assignParts, bool) {
	r := []rune(s)
	n := len(r)
	if n == 0 || (!unicode.IsLetter(r[0]) && r[0] != '_') {
		return assignParts{}, false
	}
	i := 1
	for i < n && (unicode.IsLetter(r[i]) || unicode.IsDigit(r[i]) || r[i] == '_') {
		i++
	}
	var ap assignParts
	ap.name = string(r[:i])

	if i < n && r[i] == '[' {
		depth := 1
		j := i + 1
		for j < n && depth > 0 {
			switch r[j] {
			case '[':
				depth++
			case ']':
				depth--
			}
			if depth == 0 {
				break
			}
			j++
		}
		if depth != 0 {
			return assignParts{}, false
		}
		ap.sub = string(r[i+1 : j])
		ap.hasSub = true
		i = j + 1 // skip ']'
	}

	if i < n && r[i] == '+' {
		ap.append = true
		i++
	}
	if i >= n || r[i] != '=' {
		return assignParts{}, false
	}
	ap.value = string(r[i+1:])
	return ap, true
}

// isArrayLiteral reports whether a raw assignment value is an array literal,
// i.e. wrapped in parentheses as produced by the lexer for NAME=(...).
func isArrayLiteral(v string) bool {
	return strings.HasPrefix(v, "(") && strings.HasSuffix(v, ")") && len(v) >= 2
}

// splitArrayLiteral returns the raw (still quote-protected, unexpanded) elements
// of an array literal "(e1<sep>e2...)".
func splitArrayLiteral(v string) []string {
	inner := v[1 : len(v)-1]
	if inner == "" {
		return nil
	}
	return strings.Split(inner, string(arrayElemSep))
}

// isAssoc reports whether name has been declared an associative array.
func (sh *Shell) isAssoc(name string) bool {
	_, ok := sh.assoc[name]
	return ok
}

// parseBracketKV parses an associative array literal element of the form
// [key]=value, returning the raw (still quote-protected) key and value.
func parseBracketKV(e string) (key, value string, ok bool) {
	r := []rune(e)
	if len(r) == 0 || r[0] != '[' {
		return "", "", false
	}
	depth := 1
	i := 1
	for i < len(r) && depth > 0 {
		switch r[i] {
		case '[':
			depth++
		case ']':
			depth--
		}
		if depth == 0 {
			break
		}
		i++
	}
	if depth != 0 || i+1 >= len(r) || r[i+1] != '=' {
		return "", "", false
	}
	return string(r[1:i]), string(r[i+2:]), true
}

// applyAssign performs a single assignment, dispatching between scalar, indexed-
// array, and associative-array forms (each optionally appending with +=).
func (sh *Shell) applyAssign(a string) {
	ap, ok := parseAssignParts(a)
	if !ok {
		return
	}
	assoc := sh.isAssoc(ap.name)

	// Whole-array literal: NAME=(...) or NAME+=(...).
	if !ap.hasSub && isArrayLiteral(ap.value) {
		elems := splitArrayLiteral(ap.value)
		if assoc {
			m := sh.assoc[ap.name]
			if m == nil || !ap.append {
				m = make(map[string]string)
			}
			for _, e := range elems {
				if k, v, ok := parseBracketKV(e); ok {
					m[unprotectWord(sh.expandWord(k))] = unprotectWord(sh.expandWord(v))
				}
			}
			sh.assoc[ap.name] = m
			return
		}
		var vals []string
		for _, e := range elems {
			vals = append(vals, unprotectWord(sh.expandWord(e)))
		}
		if ap.append {
			sh.arrays[ap.name] = append(sh.arrays[ap.name], vals...)
		} else {
			sh.arrays[ap.name] = vals
		}
		delete(sh.vars, ap.name)
		return
	}

	val := unprotectWord(sh.expandWord(ap.value))

	// Element assignment: NAME[sub]=val.
	if ap.hasSub {
		if assoc {
			key := unprotectWord(sh.expandWord(ap.sub))
			if ap.append {
				sh.assoc[ap.name][key] += val
			} else {
				sh.assoc[ap.name][key] = val
			}
			return
		}
		sh.arraySet(ap.name, sh.arrayIndex(ap.name, ap.sub), val, ap.append)
		return
	}

	// Scalar assignment to an existing array name targets element 0 (bash).
	if _, isArr := sh.arrays[ap.name]; isArr {
		sh.arraySet(ap.name, 0, val, ap.append)
		return
	}

	// Plain scalar. Routed through setVar (not a direct sh.vars write) so
	// SECONDS=n is actually observed -- getVar computes SECONDS dynamically
	// and never consults sh.vars for it, so a direct write here would be
	// silently invisible.
	if ap.append {
		sh.setVar(ap.name, sh.getVar(ap.name)+val)
	} else {
		sh.setVar(ap.name, val)
	}
}

// arrayIndex evaluates an array subscript expression to an integer index,
// resolving variables and arithmetic (e.g. arr[$i+1]). Negative results are
// returned as-is; arraySet/lookup interpret them relative to the end.
func (sh *Shell) arrayIndex(name, sub string) int {
	return int(evalArith(sh, sub))
}

// arraySet stores val at index idx of array name, growing the array (with empty
// elements) as needed. A negative index counts from the end. With app=true the
// value is appended to the existing element. Assigning to an element converts a
// scalar of the same name into an array.
func (sh *Shell) arraySet(name string, idx int, val string, app bool) {
	arr := sh.arrays[name]
	if arr == nil {
		if scalar, ok := sh.vars[name]; ok {
			arr = []string{scalar}
		}
	}
	if idx < 0 {
		idx += len(arr)
		if idx < 0 {
			return
		}
	}
	for len(arr) <= idx {
		arr = append(arr, "")
	}
	if app {
		arr[idx] += val
	} else {
		arr[idx] = val
	}
	sh.arrays[name] = arr
	delete(sh.vars, name)
}

// arrayValues returns the elements backing a name for read access. A scalar is
// reported as a single-element slice so ${scalar[0]} works, matching bash.
func (sh *Shell) arrayValues(name string) ([]string, bool) {
	if arr, ok := sh.arrays[name]; ok {
		return arr, true
	}
	if v, ok := sh.vars[name]; ok {
		return []string{v}, true
	}
	return nil, false
}

// expandArrayBrace handles the array forms of ${...}: ${a[i]}, ${a[@]}, ${a[*]},
// ${#a[@]} (count), ${#a[i]} (element length), and ${!a[@]} (indices). It returns
// the value and true when inner is an array reference, or ok=false to let the
// caller fall back to scalar handling.
func (sh *Shell) expandArrayBrace(inner string) (string, bool) {
	lenOp := false
	idxOp := false
	body := inner
	switch {
	case strings.HasPrefix(body, "#"):
		lenOp = true
		body = body[1:]
	case strings.HasPrefix(body, "!"):
		idxOp = true
		body = body[1:]
	}

	lb := strings.IndexByte(body, '[')
	if lb <= 0 || !strings.HasSuffix(body, "]") {
		return "", false
	}
	name := body[:lb]
	sub := body[lb+1 : len(body)-1]

	// Associative arrays: string keys, values in insertion-independent order.
	if m, ok := sh.assoc[name]; ok {
		switch sub {
		case "@", "*":
			keys := sortedKeys(m)
			if lenOp {
				return strconv.Itoa(len(keys)), true
			}
			if idxOp {
				return joinElems(keys, sub), true
			}
			vals := make([]string, len(keys))
			for i, k := range keys {
				vals[i] = m[k]
			}
			return joinElems(vals, sub), true
		default:
			key := unprotectWord(sh.expandWord(sub))
			v, exists := m[key]
			if lenOp {
				return strconv.Itoa(len([]rune(v))), true
			}
			_ = exists
			return v, true
		}
	}

	arr, _ := sh.arrayValues(name)

	switch sub {
	case "@", "*":
		if lenOp {
			return strconv.Itoa(len(arr)), true
		}
		if idxOp {
			idxs := make([]string, len(arr))
			for i := range arr {
				idxs[i] = strconv.Itoa(i)
			}
			return joinElems(idxs, sub), true
		}
		// [@] keeps element boundaries (split into words later, even when
		// quoted); [*] joins into a single space-separated field.
		return joinElems(arr, sub), true
	default:
		idx := int(evalArith(sh, sub))
		if idx < 0 {
			idx += len(arr)
		}
		if idx < 0 || idx >= len(arr) {
			if lenOp {
				return "0", true
			}
			return "", true
		}
		if lenOp {
			return strconv.Itoa(len([]rune(arr[idx]))), true
		}
		return arr[idx], true
	}
}
