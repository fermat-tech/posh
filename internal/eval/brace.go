package eval

import (
	"fmt"
	"strconv"
	"strings"
)

// braceExpand expands a single word token into one or more words.
// Handles {a,b,c}, {1..N}, {a..z}, zero-padded {01..10}, step {1..10..2},
// prefix/suffix (pre{a,b}suf), nested ({a,{b,c}}), and chained ({a,b}{1,2}).
func braceExpand(word string) []string {
	return braceExpandWord([]rune(word))
}

func braceExpandWord(runes []rune) []string {
	open := findOpenBrace(runes)
	if open < 0 {
		return []string{string(runes)}
	}
	close := findCloseBrace(runes, open)
	if close < 0 {
		return []string{string(runes)}
	}

	prefix := runes[:open]
	inner := runes[open+1 : close]
	suffix := runes[close+1:]

	// Try {x..y} or {x..y..step} range first.
	if expanded := tryRangeExpand(string(inner)); expanded != nil {
		var result []string
		for _, e := range expanded {
			combined := append(append(append([]rune(nil), prefix...), []rune(e)...), suffix...)
			result = append(result, braceExpandWord(combined)...)
		}
		return result
	}

	// Comma-separated list.
	items := splitBraceItems(inner)
	if len(items) < 2 {
		return []string{string(runes)} // literal — not a valid expansion
	}

	var result []string
	for _, item := range items {
		combined := append(append(append([]rune(nil), prefix...), item...), suffix...)
		result = append(result, braceExpandWord(combined)...)
	}
	return result
}

// findOpenBrace returns the rune index of the first unescaped {.
func findOpenBrace(runes []rune) int {
	for i, ch := range runes {
		if ch == '{' {
			return i
		}
	}
	return -1
}

// findCloseBrace returns the index of the } matching the { at open.
func findCloseBrace(runes []rune, open int) int {
	depth := 0
	for i := open; i < len(runes); i++ {
		switch runes[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitBraceItems splits the inner content of {} by commas at depth 0.
func splitBraceItems(runes []rune) [][]rune {
	var items [][]rune
	depth := 0
	start := 0
	for i, ch := range runes {
		switch ch {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 {
				items = append(items, runes[start:i])
				start = i + 1
			}
		}
	}
	items = append(items, runes[start:])
	return items
}

// tryRangeExpand attempts to expand s as {from..to} or {from..to..step}.
// Returns nil if s is not a valid range expression.
func tryRangeExpand(s string) []string {
	parts := strings.SplitN(s, "..", 3)
	if len(parts) < 2 {
		return nil
	}
	from, to := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	step := 1
	if len(parts) == 3 {
		n, err := strconv.Atoi(strings.TrimSpace(parts[2]))
		if err != nil || n == 0 {
			return nil
		}
		if n < 0 {
			n = -n
		}
		step = n
	}

	// Integer range.
	fromN, err1 := strconv.Atoi(from)
	toN, err2 := strconv.Atoi(to)
	if err1 == nil && err2 == nil {
		// Detect zero-padding width.
		width := 0
		if (strings.HasPrefix(from, "0") && len(from) > 1) ||
			(strings.HasPrefix(to, "0") && len(to) > 1) {
			if len(from) >= len(to) {
				width = len(from)
			} else {
				width = len(to)
			}
		}
		var result []string
		if fromN <= toN {
			for i := fromN; i <= toN; i += step {
				if width > 0 {
					result = append(result, fmt.Sprintf("%0*d", width, i))
				} else {
					result = append(result, strconv.Itoa(i))
				}
			}
		} else {
			for i := fromN; i >= toN; i -= step {
				if width > 0 {
					result = append(result, fmt.Sprintf("%0*d", width, i))
				} else {
					result = append(result, strconv.Itoa(i))
				}
			}
		}
		return result
	}

	// Character range (single rune on each side).
	fromR, toR := []rune(from), []rune(to)
	if len(fromR) == 1 && len(toR) == 1 {
		f, t := fromR[0], toR[0]
		var result []string
		if f <= t {
			for ch := f; ch <= t; ch += rune(step) {
				result = append(result, string(ch))
			}
		} else {
			for ch := f; ch >= t; ch -= rune(step) {
				result = append(result, string(ch))
			}
		}
		return result
	}

	return nil
}
