package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fermat-tech/posh/internal/eval"
	"github.com/fermat-tech/posh/internal/parser"
	"github.com/mattn/go-colorable"
	"github.com/mattn/go-isatty"
	"github.com/peterh/liner"
)

var progName string

func init() {
	name := filepath.Base(os.Args[0])
	progName = strings.TrimSuffix(name, filepath.Ext(name))
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

	// Keep the shell alive across Ctrl+C for the whole session. A foreground
	// external command forwards the interrupt to its own process group (see
	// eval.evalSimpleCmd), but the shell itself must never take the default
	// terminate action. Registering a persistent handler keeps os.Interrupt
	// "wanted" so the Windows console control handler always reports the event
	// as handled — even at the prompt, between command runs, or when a
	// background job also receives the console's CTRL_C_EVENT. The goroutine
	// just drains the channel; the line editors handle Ctrl+C as a line-abort
	// while reading input. Foreground commands additionally Notify their own
	// channel, so this does not interfere with interrupt forwarding.
	sessionSig := make(chan os.Signal, 1)
	signal.Notify(sessionSig, os.Interrupt)
	go func() {
		for range sessionSig {
		}
	}()

	args := os.Args[1:]
	sh := eval.New(progName)
	sh.Stdout = colorStdout
	sh.Stderr = colorStderr

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

	// Track whether command output ended on a partial line (no trailing newline,
	// e.g. printf "%s "). This does not alter any output — it only lets the REPL
	// move to a fresh line before drawing the next prompt so partial-line output
	// isn't overwritten by the prompt's redraw.
	out := &newlineTracker{w: sh.Stdout, atLineStart: true}
	sh.Stdout = out

	c := &poshCompleter{sh: sh}

	// openLiner / closeLiner manage a single liner instance.
	// liner and the vi raw-mode editor must never be open at the same time.
	var rl *liner.State

	openLiner := func() {
		if rl != nil {
			return
		}
		rl = liner.NewLiner()
		rl.SetCtrlCAborts(true)
		for _, h := range sh.History {
			rl.AppendHistory(h)
		}
		rl.SetWordCompleter(c.Complete)
	}

	closeLiner := func() {
		if rl == nil {
			return
		}
		rl.Close()
		rl = nil
	}

	defer closeLiner()
	// Persist history from sh.History, which both the emacs (liner) and vi
	// editors append to. Saving here rather than via liner's WriteHistory means
	// history is written in every mode and on every exit path (Ctrl+D, exit, or
	// EOF) — the deferred call runs during the exit panic's unwinding too.
	defer saveHistory(histFile, sh)

	// Pre-load history from file into sh.History so the vi editor can use it.
	if f, err := os.Open(histFile); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if line := sc.Text(); line != "" {
				sh.History = append(sh.History, line)
			}
		}
		f.Close()
	}

	// Open liner now only if not starting in vi mode.
	if !sh.GetOpt("vi") {
		openLiner()
	}

	for {
		// If the previous command left output on a partial line (no trailing
		// newline), move to a fresh line so the prompt doesn't overwrite it.
		if !out.atLineStart {
			io.WriteString(out, "\n")
		}

		var input string
		var err error

		if sh.GetOpt("vi") {
			closeLiner() // release terminal if we just switched from emacs
			input, err = viReadMultiLine(sh, c.Complete)
		} else {
			openLiner() // acquire terminal if we just switched from vi
			input, err = linerReadMultiLine(rl, sh)
		}

		if isViInterrupt(err) || err == liner.ErrPromptAborted {
			fmt.Fprintln(sh.Stderr)
			continue
		}
		if isViEOF(err) || err == io.EOF {
			fmt.Fprintln(sh.Stdout)
			break
		}

		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		if rl != nil {
			rl.AppendHistory(input)
		}
		sh.History = append(sh.History, input)
		sh.EvalString(input)
		os.Stdout.Sync()
	}
}

// newlineTracker wraps a writer and records whether the most recently written
// byte was a newline. It passes all bytes through unchanged; the REPL uses
// atLineStart to decide whether to emit a separating newline before the prompt
// so that partial-line command output (e.g. printf without a trailing newline)
// is not overwritten.
type newlineTracker struct {
	w           io.Writer
	atLineStart bool
}

func (t *newlineTracker) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	if n > 0 {
		t.atLineStart = p[n-1] == '\n'
	}
	return n, err
}

// maxHistory caps how many of the most recent entries are written to the
// history file (mirrors the documented limit).
const maxHistory = 1000

// saveHistory writes the shell's command history to path (most recent entries
// last), keeping at most maxHistory lines. Failures are silent so an unwritable
// home directory never disrupts exit.
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
		fmt.Fprintln(w, line)
	}
	w.Flush()
}

// linerReadMultiLine reads one complete command using liner (emacs mode).
func linerReadMultiLine(rl *liner.State, sh *eval.Shell) (string, error) {
	var lines []string
	for {
		prompt := buildPrompt(sh)
		if len(lines) > 0 {
			prompt = "> "
		}
		line, err := rl.Prompt(prompt)
		if err != nil {
			if len(lines) == 0 {
				return "", err
			}
			return strings.Join(lines, "\n"), err
		}
		lines = append(lines, line)
		full := strings.Join(lines, "\n")
		if !parser.NeedsContinuation(full) {
			return full, nil
		}
	}
}

// viReadMultiLine reads one complete command using the vi-mode editor. The
// editor manages the whole (possibly multi-line) command as a single buffer:
// when Enter is pressed while the command is incomplete, it inserts a newline
// and keeps editing, so vi motions work across the entire command.
func viReadMultiLine(sh *eval.Shell, completer completeFn) (string, error) {
	return viReadLine(buildPrompt(sh), "> ", sh.History, completer, parser.NeedsContinuation)
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
