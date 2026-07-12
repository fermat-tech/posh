package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fermat-tech/posh/internal/eval"
	"github.com/fermat-tech/posh/internal/parser"
	"github.com/mattn/go-colorable"
	"github.com/mattn/go-isatty"
)

var progName string

func init() {
	name := filepath.Base(os.Args[0])
	progName = strings.TrimSuffix(name, filepath.Ext(name))
}

// version is the release tag (e.g. "v1.3.49"). Overridden at build time via
// -ldflags "-X main.version=vX.Y.Z"; "dev" for a plain `go build` with no tag
// supplied.
var version = "dev"

// printVersion mirrors `bash --version`'s layout, using progName (the stem of
// the invoked path, per posh's rename-friendly identity convention — see
// CLAUDE.md) instead of a fixed "posh", and this project's actual license
// instead of bash's GPL.
func printVersion() {
	fmt.Fprintf(colorStdout, `%s, version %s
Copyright (c) 2025 fermat-tech
License: MIT <https://opensource.org/licenses/MIT>

This is free software; you are free to change and redistribute it.
There is NO WARRANTY, to the extent permitted by law.
`, progName, version)
}

var colorStdout = colorable.NewColorableStdout()
var colorStderr = colorable.NewColorableStderr()

func main() {
	// A top-level `exit` unwinds here as a shellExit panic. Recover it and exit
	// the process with the requested status; deferred cleanup (e.g. history
	// saving in the REPL) has already run during unwinding. Scripts and
	// subshells contain their own `exit` lower down, so only a session-level
	// exit reaches this point.
	defer func() {
		if r := recover(); r != nil {
			if code, ok := eval.ExitCode(r); ok {
				os.Exit(code)
			}
			panic(r)
		}
	}()

	// Keep the shell alive across Ctrl+C for the whole session, and let a
	// foreground loop/statement list notice the interrupt and stop running (see
	// eval.WatchInterrupts / eval.EvalStringAt) instead of silently continuing
	// to its next iteration or statement. A foreground external command
	// additionally forwards the interrupt directly to its own process group
	// (see eval.evalSimpleCmd), which is what makes something like `sleep 5`
	// stop immediately; this handler is what then lets the loop containing it
	// stop too, matching bash. The shell itself must never take the default
	// terminate action — registering a persistent handler keeps os.Interrupt
	// "wanted" so the Windows console control handler always reports the event
	// as handled, even at the prompt or when a background job also receives the
	// console's CTRL_C_EVENT. The line editors separately handle Ctrl+C as a
	// line-abort while reading input, which this does not interfere with.
	eval.WatchInterrupts()

	args := os.Args[1:]

	if len(args) >= 1 && args[0] == "--version" {
		printVersion()
		os.Exit(0)
	}

	sh := eval.New(progName)
	sh.SetVersion(version)
	sh.Stdout = colorStdout
	sh.Stderr = colorStderr
	// Remember the raw console handles so foreground children can inherit a real
	// TTY (enabling git's color and pager) instead of the colorable wrapper,
	// which would otherwise force an OS pipe. Only when output is a terminal.
	if isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd()) {
		sh.ConsoleOut = os.Stdout
		sh.TermOut = colorStdout
	}
	if isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd()) {
		sh.ConsoleErr = os.Stderr
		sh.TermErr = colorStderr
	}

	// -c "command"
	if len(args) >= 2 && args[0] == "-c" {
		code := sh.EvalString(args[1])
		os.Exit(code)
	}

	// Script file
	if len(args) >= 1 && !strings.HasPrefix(args[0], "-") {
		data, err := os.ReadFile(args[0])
		if err != nil {
			fmt.Fprintf(colorStderr, "%s: cannot open %q: %v\n", progName, args[0], err)
			os.Exit(1)
		}
		src := strings.ReplaceAll(string(data), "\r\n", "\n")
		// $0 is the script's path (as bash does), and error messages emitted while
		// the script runs are prefixed with it rather than the interpreter name.
		sh.SetName(args[0])
		sh.SetPosParams(args[1:])
		code := sh.EvalString(src)
		os.Exit(code)
	}

	runREPL(sh)
}

func runREPL(sh *eval.Shell) {
	// Source ~/.poshrc
	home, _ := os.UserHomeDir()
	rcPath := filepath.Join(home, ".poshrc")
	if data, err := os.ReadFile(rcPath); err == nil {
		sh.EvalString(string(data))
	}

	histFile := filepath.Join(home, ".posh_history")
	isInteractive := isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())

	if !isInteractive {
		runNonInteractive(sh)
		return
	}

	c := &poshCompleter{sh: sh}

	// Persist history on every exit path (Ctrl+D, EOF, or the exit builtin, whose
	// panic unwinds through this defer).
	defer saveHistory(histFile, sh)

	// Pre-load history from the file into sh.History.
	sh.History = append(sh.History, loadHistory(histFile)...)

	for {
		// Both editing modes use the same editor; emacs mode is simply vi mode
		// turned off (insert-only, with emacs keybindings).
		emacs := !sh.GetOpt("vi")
		input, err := viReadMultiLine(sh, c.Complete, emacs)

		if isViInterrupt(err) {
			fmt.Fprintln(sh.Stderr)
			continue
		}
		if isViEOF(err) {
			fmt.Fprintln(sh.Stdout)
			break
		}

		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		sh.History = append(sh.History, input)
		sh.EvalString(input)
		os.Stdout.Sync()
	}
}

// maxHistory caps how many of the most recent entries are written to the
// history file (mirrors the documented limit).
const maxHistory = 1000

// saveHistory writes the shell's command history to path (most recent entries
// last), keeping at most maxHistory entries. Each entry is encoded onto a single
// physical line so multi-line commands (heredocs, quoted strings spanning lines)
// round-trip as one history entry. Failures are silent so an unwritable home
// directory never disrupts exit.
func saveHistory(path string, sh *eval.Shell) {
	hist := sh.History
	if len(hist) > maxHistory {
		hist = hist[len(hist)-maxHistory:]
	}
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, line := range hist {
		fmt.Fprintln(w, encodeHistoryLine(line))
	}
	w.Flush()
}

// loadHistory reads the history file, decoding the single-line encoding so that
// multi-line commands are restored as one entry each. Returns nil if the file
// can't be read.
func loadHistory(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var hist []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // allow long encoded lines
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			hist = append(hist, decodeHistoryLine(line))
		}
	}
	return hist
}

// encodeHistoryLine escapes a history entry so it occupies a single physical
// line in the history file: backslash, newline, and carriage return become \\,
// \n, and \r. decodeHistoryLine reverses it.
func encodeHistoryLine(s string) string {
	if !strings.ContainsAny(s, "\\\n\r") {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// decodeHistoryLine reverses encodeHistoryLine, restoring embedded newlines.
// Unrecognized escapes are left as-is, so plain entries from older history files
// (no escaping) load unchanged.
func decodeHistoryLine(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == '\\' && i+1 < len(rs) {
			switch rs[i+1] {
			case 'n':
				b.WriteByte('\n')
				i++
				continue
			case 'r':
				b.WriteByte('\r')
				i++
				continue
			case '\\':
				b.WriteByte('\\')
				i++
				continue
			}
		}
		b.WriteRune(rs[i])
	}
	return b.String()
}

// viReadMultiLine reads one complete command using posh's line editor. The
// editor manages the whole (possibly multi-line) command as a single buffer:
// when Enter is pressed while the command is incomplete, it inserts a newline
// and keeps editing. emacs selects emacs-style key bindings (insert-only with
// Ctrl-key editing); otherwise it is the modal vi editor.
func viReadMultiLine(sh *eval.Shell, completer completeFn, emacs bool) (string, error) {
	return viReadLine(buildPrompt(sh), "> ", sh.History, completer, parser.NeedsContinuation, emacs)
}

func runNonInteractive(sh *eval.Shell) {
	sc := bufio.NewScanner(os.Stdin)
	var lines []string
	lineBase := 1  // absolute line number of first line in current chunk
	linesRead := 0 // total lines consumed so far
	for sc.Scan() {
		line := sc.Text()
		linesRead++
		lines = append(lines, line)
		full := strings.Join(lines, "\n")
		if parser.NeedsContinuation(full) {
			continue
		}
		trimmed := strings.TrimSpace(full)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			code := sh.EvalStringAt(full, lineBase)
			if sh.HasParseError() {
				os.Exit(code)
			}
		}
		lineBase = linesRead + 1
		lines = nil
	}
	if len(lines) > 0 {
		code := sh.EvalStringAt(strings.Join(lines, "\n"), lineBase)
		if sh.HasParseError() {
			os.Exit(code)
		}
	}
}

func runDumb(sh *eval.Shell) {
	sc := bufio.NewScanner(os.Stdin)
	var lines []string
	for {
		if len(lines) == 0 {
			fmt.Fprint(sh.Stdout, buildPrompt(sh))
		} else {
			fmt.Fprint(sh.Stdout, "> ")
		}
		if !sc.Scan() {
			break
		}
		line := sc.Text()
		lines = append(lines, line)
		full := strings.Join(lines, "\n")
		if parser.NeedsContinuation(full) {
			continue
		}
		trimmed := strings.TrimSpace(full)
		if trimmed != "" {
			sh.History = append(sh.History, trimmed)
			sh.EvalString(full)
		}
		lines = nil
	}
}

// ---- tab completion (liner WordCompleter) ----

type poshCompleter struct {
	sh *eval.Shell
}

// findWordStart scans head left-to-right respecting quotes and returns the
// start index of the current (last) word and the open-quote character if the
// word started with an unmatched " or '.  A zero openQuote means the word is
// unquoted or the quote was already closed.
func findWordStart(head string) (start int, openQuote rune) {
	runes := []rune(head)
	n := len(runes)
	wordStart := 0
	var oq rune
	i := 0
	for i < n {
		ch := runes[i]
		switch {
		case ch == ' ' || ch == '\t' || ch == '|' || ch == '&' || ch == ';' || ch == '(':
			i++
			wordStart = i
			oq = 0
		case ch == '"' || ch == '\'':
			wordStart = i
			oq = ch
			i++
			for i < n && runes[i] != ch {
				i++
			}
			if i < n { // found matching close quote — word finished cleanly
				i++
				oq = 0
			}
			// if i >= n the quote is still open; oq stays set
		default:
			i++
		}
	}
	return wordStart, oq
}

// looksLikePath reports whether a word in command position should be completed
// as a filename rather than a command name. Mirrors bash: a command word that
// contains a slash (or begins with ~) is a pathname, so it gets file completion
// instead of a PATH/builtin lookup.
func looksLikePath(word string) bool {
	return strings.ContainsAny(word, "/\\") || strings.HasPrefix(word, "~")
}

func (c *poshCompleter) Complete(line string, pos int) (string, []string, string) {
	head := line[:pos]
	tail := line[pos:]

	wordStart, openQuote := findWordStart(head)
	prefix := head[wordStart:]

	// Strip surrounding quotes so matching works on the raw path/word.
	rawPrefix := prefix
	if len(rawPrefix) > 0 && (rawPrefix[0] == '"' || rawPrefix[0] == '\'') {
		rawPrefix = rawPrefix[1:]
		if len(rawPrefix) > 0 && rawPrefix[len(rawPrefix)-1] == prefix[0] {
			rawPrefix = rawPrefix[:len(rawPrefix)-1]
		}
	}

	before := strings.TrimRight(head[:wordStart], " \t")
	isFirstWord := before == "" || (len(before) > 0 &&
		strings.ContainsAny(string([]rune(before)[len([]rune(before))-1:]), "|&;("))

	var completions []string
	switch {
	case strings.HasPrefix(rawPrefix, "$") && !strings.ContainsAny(rawPrefix, "/\\"):
		completions = c.varCandidates(rawPrefix)
	case isFirstWord && openQuote == 0 && !looksLikePath(rawPrefix):
		completions = c.commandCandidates(rawPrefix)
	default:
		completions = c.fileCandidates(rawPrefix)
		// If trailing whitespace yielded no matches, retry without it.
		if len(completions) == 0 {
			trimmed := strings.TrimRight(rawPrefix, " \t")
			if trimmed != rawPrefix {
				completions = c.fileCandidates(trimmed)
			}
		}
	}
	sort.Strings(completions)
	// head returned to caller excludes the opening quote; doComplete / liner
	// will re-add quotes around the chosen completion as needed.
	return head[:wordStart], completions, tail
}

func (c *poshCompleter) varCandidates(prefix string) []string {
	varPrefix := strings.ToLower(prefix[1:]) // strip leading $
	var out []string
	seen := make(map[string]bool)
	for k := range c.sh.Vars() {
		if strings.HasPrefix(strings.ToLower(k), varPrefix) && !seen[k] {
			seen[k] = true
			out = append(out, "$"+k)
		}
	}
	// Array variables (e.g. POSH_VERSINFO) live in a separate map from scalars,
	// so Vars() alone would never offer them — without this, a prefix matching
	// both a scalar and an array variable (like BASH_VERSI matching both
	// BASH_VERSINFO and BASH_VERSION in bash) would resolve as a single match
	// instead of listing both alternatives.
	for _, k := range c.sh.ArrayNames() {
		if strings.HasPrefix(strings.ToLower(k), varPrefix) && !seen[k] {
			seen[k] = true
			out = append(out, "$"+k)
		}
	}
	// Also include exported OS env vars
	for _, kv := range os.Environ() {
		k := strings.SplitN(kv, "=", 2)[0]
		if strings.HasPrefix(strings.ToLower(k), varPrefix) && !seen[k] {
			seen[k] = true
			out = append(out, "$"+k)
		}
	}
	return out
}

var builtinNames = []string{
	"cd", "pwd", "echo", "printf", "exit", "export", "unset", "env", "set",
	"source", ".", "alias", "unalias", "history", "type", "help", "true",
	"false", ":", "jobs", "fg", "bg", "wait", "kill", "ps", "eval", "mkdir", "test", "[", "break",
	"continue", "return", "read", "shift", "trap", "clear",
	"ls", "wc", "which", "grep", "egrep", "find", "head", "tail", "less",
}

func (c *poshCompleter) commandCandidates(prefix string) []string {
	seen := make(map[string]bool)
	var out []string

	for _, b := range builtinNames {
		if strings.HasPrefix(b, prefix) && !seen[b] {
			seen[b] = true
			out = append(out, b)
		}
	}

	pathExts := strings.Split(os.Getenv("PATHEXT"), string(os.PathListSeparator))
	for _, dir := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			base := strings.ToLower(name)
			for _, ext := range pathExts {
				if strings.HasSuffix(base, strings.ToLower(ext)) {
					stem := name[:len(name)-len(ext)]
					if strings.HasPrefix(strings.ToLower(stem), strings.ToLower(prefix)) && !seen[stem] {
						seen[stem] = true
						out = append(out, stem)
					}
				}
			}
			if strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) && !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

func (c *poshCompleter) fileCandidates(prefix string) []string {
	// Split prefix into directory and base parts at the last path separator.
	var dirPart, basePart string
	lastSep := strings.LastIndexAny(prefix, "/\\")
	if lastSep >= 0 {
		dirPart = prefix[:lastSep+1]
		basePart = prefix[lastSep+1:]
	} else {
		dirPart = ""
		basePart = prefix
	}

	// Expand variables in the directory part so $VAR/path works.
	expandedDir := c.sh.ExpandWord(strings.TrimRight(dirPart, "/\\"))
	if expandedDir == "" {
		expandedDir = "."
	}

	entries, err := os.ReadDir(expandedDir)
	if err != nil {
		return nil
	}

	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(basePart)) {
			continue
		}
		candidate := dirPart + name
		if e.IsDir() {
			candidate += "/"
		}
		out = append(out, candidate)
	}
	return out
}

// ---- prompt ----

func buildPrompt(sh *eval.Shell) string {
	ps1 := sh.GetVar("PS1")
	if ps1 == "" {
		ps1 = `\u@\h \w \$ `
	}
	return expandPS1(ps1, sh)
}

func expandPS1(ps1 string, sh *eval.Shell) string {
	var sb strings.Builder
	runes := []rune(ps1)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '\\' || i+1 >= len(runes) {
			sb.WriteRune(runes[i])
			continue
		}
		i++
		switch runes[i] {
		case 'u':
			sb.WriteString(currentUser())
		case 'h':
			sb.WriteString(shortHostname())
		case 'H':
			sb.WriteString(fullHostname())
		case 'w':
			sb.WriteString(cwdTilde(sh))
		case 'W':
			wd, _ := os.Getwd()
			sb.WriteString(filepath.Base(wd))
		case '$':
			if os.Getuid() == 0 {
				sb.WriteByte('#')
			} else {
				sb.WriteByte('$')
			}
		case 'n':
			sb.WriteByte('\n')
		case '\\':
			sb.WriteByte('\\')
		default:
			sb.WriteByte('\\')
			sb.WriteRune(runes[i])
		}
	}
	return sb.String()
}

func currentUser() string {
	if u := os.Getenv("USERNAME"); u != "" {
		return u
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "user"
}

func shortHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "localhost"
	}
	if idx := strings.IndexByte(h, '.'); idx > 0 {
		return h[:idx]
	}
	return h
}

func fullHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "localhost"
	}
	addrs, err := net.LookupHost(h)
	if err != nil || len(addrs) == 0 {
		return h
	}
	names, err := net.LookupAddr(addrs[0])
	if err != nil || len(names) == 0 {
		return h
	}
	return strings.TrimSuffix(names[0], ".")
}

func cwdTilde(sh *eval.Shell) string {
	wd, err := os.Getwd()
	if err != nil {
		return "?"
	}
	home := sh.GetVar("HOME")
	if home == "" {
		h, _ := os.UserHomeDir()
		home = h
	}
	if home != "" && strings.HasPrefix(wd, home) {
		return "~" + wd[len(home):]
	}
	return wd
}
