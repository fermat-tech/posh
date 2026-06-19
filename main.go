package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
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
		sh.SetPosParams(args[1:])
		code := sh.EvalString(string(data))
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

	rl := liner.NewLiner()
	defer func() {
		if f, err := os.Create(histFile); err == nil {
			rl.WriteHistory(f)
			f.Close()
		}
		rl.Close()
	}()

	rl.SetCtrlCAborts(true)

	// Load history
	if f, err := os.Open(histFile); err == nil {
		rl.ReadHistory(f)
		f.Close()
	}

	// Tab completion
	c := &poshCompleter{sh: sh}
	rl.SetWordCompleter(c.Complete)

	for {
		input, err := readMultiLine(rl, sh)
		if err == liner.ErrPromptAborted {
			fmt.Fprintln(sh.Stderr)
			continue
		}
		if err == io.EOF {
			fmt.Fprintln(sh.Stdout)
			break
		}
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		rl.AppendHistory(input)
		sh.History = append(sh.History, input)
		sh.EvalString(input)
	}
}

// readMultiLine reads one complete shell command, prompting for more lines
// when the input appears incomplete (open compound commands, trailing operators).
func readMultiLine(rl *liner.State, sh *eval.Shell) (string, error) {
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

func runNonInteractive(sh *eval.Shell) {
	sc := bufio.NewScanner(os.Stdin)
	var lines []string
	for sc.Scan() {
		line := sc.Text()
		lines = append(lines, line)
		full := strings.Join(lines, "\n")
		if parser.NeedsContinuation(full) {
			continue
		}
		trimmed := strings.TrimSpace(full)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			sh.EvalString(full)
		}
		lines = nil
	}
	if len(lines) > 0 {
		sh.EvalString(strings.Join(lines, "\n"))
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

// ---- tab completion ----

type poshCompleter struct {
	sh *eval.Shell
}

// Complete implements liner.WordCompleter.
// Returns (head, completions, tail) where head is line[:wordStart],
// completions are the full candidates, and tail is line[pos:].
func (c *poshCompleter) Complete(line string, pos int) (string, []string, string) {
	head := line[:pos]
	tail := line[pos:]
	wordStart := strings.LastIndexAny(head, " \t|&;(") + 1
	prefix := head[wordStart:]
	isFirstWord := strings.TrimSpace(head[:wordStart]) == "" ||
		strings.ContainsAny(strings.TrimRight(head[:wordStart], " \t"), "|&;(")

	var candidates []string
	if isFirstWord {
		candidates = c.commandCandidates(prefix)
	} else {
		candidates = c.fileCandidates(prefix)
	}
	sort.Strings(candidates)

	var completions []string
	for _, cand := range candidates {
		if strings.HasPrefix(cand, prefix) {
			completions = append(completions, cand)
		}
	}
	return head[:wordStart], completions, tail
}

var builtinNames = []string{
	"cd", "pwd", "echo", "printf", "exit", "export", "unset", "env", "set",
	"source", ".", "alias", "unalias", "history", "type", "help", "true",
	"false", ":", "jobs", "fg", "bg", "wait", "test", "[", "break",
	"continue", "return", "read", "shift", "trap", "clear",
	"ls", "wc", "which", "grep", "egrep", "find", "head", "tail", "less",
}

func (c *poshCompleter) commandCandidates(prefix string) []string {
	seen := make(map[string]bool)
	var out []string

	// Builtins
	for _, b := range builtinNames {
		if strings.HasPrefix(b, prefix) && !seen[b] {
			seen[b] = true
			out = append(out, b)
		}
	}

	// PATH executables
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
	dir := filepath.Dir(prefix)
	base := filepath.Base(prefix)
	if prefix == "" || strings.HasSuffix(prefix, "/") || strings.HasSuffix(prefix, "\\") {
		dir = prefix
		base = ""
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		entries, err = os.ReadDir(".")
		if err != nil {
			return nil
		}
		dir = "."
	}

	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(base)) {
			continue
		}
		candidate := filepath.Join(dir, name)
		if dir == "." && !strings.Contains(prefix, "/") && !strings.Contains(prefix, "\\") {
			candidate = name
		}
		if e.IsDir() {
			candidate += string(os.PathSeparator)
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
