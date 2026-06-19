package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/chzyer/readline"
	"github.com/fermat-tech/posh/internal/eval"
	"github.com/mattn/go-colorable"
	"github.com/mattn/go-isatty"
)

var progName string

func init() {
	name := filepath.Base(os.Args[0])
	progName = strings.TrimSuffix(name, filepath.Ext(name))
}

var colorStdout = colorable.NewColorableStdout()
var colorStderr = colorable.NewColorableStderr()

func useColor() bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	return isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
}

func main() {
	args := os.Args[1:]
	sh := eval.New(progName)
	sh.Stdout = colorStdout
	sh.Stderr = colorStderr

	// -c "command"
	if len(args) >= 2 && args[0] == "-c" {
		cmd := args[1]
		code := sh.EvalString(cmd)
		os.Exit(code)
	}

	// Script file
	if len(args) >= 1 && !strings.HasPrefix(args[0], "-") {
		data, err := os.ReadFile(args[0])
		if err != nil {
			fmt.Fprintf(colorStderr, "%s: cannot open %q: %v\n", progName, args[0], err)
			os.Exit(1)
		}
		code := sh.EvalString(string(data))
		os.Exit(code)
	}

	// Interactive REPL
	runREPL(sh)
}

func runREPL(sh *eval.Shell) {
	// Source ~/.poshrc if it exists
	home, _ := os.UserHomeDir()
	rcPath := filepath.Join(home, ".poshrc")
	if data, err := os.ReadFile(rcPath); err == nil {
		sh.EvalString(string(data))
	}

	histFile := filepath.Join(home, ".posh_history")
	isInteractive := isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())

	if !isInteractive {
		// Non-interactive: read from stdin line by line
		runNonInteractive(sh)
		return
	}

	rl, err := readline.NewEx(&readline.Config{
		HistoryFile:     histFile,
		HistoryLimit:    1000,
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
	})
	if err != nil {
		// Fallback to dumb line reader
		runDumb(sh)
		return
	}
	defer rl.Close()

	// Forward sigint to the readline instance (interrupts current read, not the shell)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	go func() {
		for range sigCh {
			rl.SetPrompt(buildPrompt(sh))
			rl.Refresh()
		}
	}()

	for {
		rl.SetPrompt(buildPrompt(sh))
		line, err := rl.Readline()
		if err == readline.ErrInterrupt {
			fmt.Fprintln(sh.Stderr)
			continue
		}
		if err == io.EOF {
			fmt.Fprintln(sh.Stdout)
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sh.History = append(sh.History, line)
		sh.EvalString(line)
	}
}

func runNonInteractive(sh *eval.Shell) {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sh.EvalString(line)
	}
}

func runDumb(sh *eval.Shell) {
	sc := bufio.NewScanner(os.Stdin)
	for {
		fmt.Fprint(sh.Stdout, buildPrompt(sh))
		if !sc.Scan() {
			break
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		sh.History = append(sh.History, line)
		sh.EvalString(line)
	}
}

// buildPrompt renders the PS1 prompt string.
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
			if os.Geteuid() == 0 {
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
