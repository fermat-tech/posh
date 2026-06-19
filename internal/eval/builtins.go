package eval

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// builtinFn is the signature for all built-in command implementations.
type builtinFn func(sh *Shell, args []string, stdin io.Reader, stdout, stderr io.Writer) int

// builtins maps command name → implementation.
var builtins map[string]builtinFn

func init() {
	builtins = map[string]builtinFn{
		"cd":      builtinCd,
		"pwd":     builtinPwd,
		"echo":    builtinEcho,
		"printf":  builtinPrintf,
		"exit":    builtinExit,
		"export":  builtinExport,
		"unset":   builtinUnset,
		"env":     builtinEnv,
		"set":     builtinSet,
		"source":  builtinSource,
		".":       builtinSource,
		"alias":   builtinAlias,
		"unalias": builtinUnalias,
		"history": builtinHistory,
		"type":    builtinType,
		"help":    builtinHelp,
		"true":    func(_ *Shell, _ []string, _ io.Reader, _, _ io.Writer) int { return 0 },
		"false":   func(_ *Shell, _ []string, _ io.Reader, _, _ io.Writer) int { return 1 },
		":":       func(_ *Shell, _ []string, _ io.Reader, _, _ io.Writer) int { return 0 },
		"jobs":    builtinJobs,
	}
}

// ---- implementations ----

func builtinCd(sh *Shell, args []string, _ io.Reader, _, stderr io.Writer) int {
	var dir string
	switch len(args) {
	case 0:
		dir = sh.homeDir()
	case 1:
		dir = sh.expandWord(args[0])
	default:
		fmt.Fprintf(stderr, "cd: too many arguments\n")
		return 1
	}
	if err := os.Chdir(dir); err != nil {
		fmt.Fprintf(stderr, "cd: %v\n", err)
		return 1
	}
	wd, _ := os.Getwd()
	sh.setVar("PWD", wd)
	return 0
}

func builtinPwd(_ *Shell, _ []string, _ io.Reader, stdout, stderr io.Writer) int {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "pwd: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, wd)
	return 0
}

func builtinEcho(_ *Shell, args []string, _ io.Reader, stdout, _ io.Writer) int {
	noNewline := false
	start := 0
	if len(args) > 0 && args[0] == "-n" {
		noNewline = true
		start = 1
	}
	out := strings.Join(args[start:], " ")
	if noNewline {
		fmt.Fprint(stdout, out)
	} else {
		fmt.Fprintln(stdout, out)
	}
	return 0
}

func builtinPrintf(_ *Shell, args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "printf: missing format string\n")
		return 1
	}
	format := args[0]
	fmtArgs := make([]any, len(args)-1)
	for i, a := range args[1:] {
		fmtArgs[i] = a
	}
	// Replace shell-style %s with Go's — they are already compatible
	// Handle \n \t escapes in format
	format = strings.ReplaceAll(format, `\n`, "\n")
	format = strings.ReplaceAll(format, `\t`, "\t")
	format = strings.ReplaceAll(format, `\r`, "\r")
	fmt.Fprintf(stdout, format, fmtArgs...)
	return 0
}

func builtinExit(_ *Shell, args []string, _ io.Reader, _, _ io.Writer) int {
	code := 0
	if len(args) > 0 {
		n, err := strconv.Atoi(args[0])
		if err == nil {
			code = n
		}
	}
	os.Exit(code)
	return code
}

func builtinExport(sh *Shell, args []string, _ io.Reader, _, stderr io.Writer) int {
	if len(args) == 0 {
		// print all exported vars
		names := make([]string, 0, len(sh.exported))
		for k := range sh.exported {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			fmt.Fprintf(os.Stdout, "export %s=%q\n", k, sh.vars[k])
		}
		return 0
	}
	for _, a := range args {
		idx := strings.IndexByte(a, '=')
		if idx >= 0 {
			sh.setVar(a[:idx], a[idx+1:])
			sh.exported[a[:idx]] = true
		} else {
			sh.exported[a] = true
		}
	}
	return 0
}

func builtinUnset(sh *Shell, args []string, _ io.Reader, _, _ io.Writer) int {
	for _, a := range args {
		delete(sh.vars, a)
		delete(sh.exported, a)
	}
	return 0
}

func builtinEnv(sh *Shell, _ []string, _ io.Reader, stdout, _ io.Writer) int {
	names := make([]string, 0, len(sh.vars))
	for k := range sh.vars {
		if sh.exported[k] {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	for _, k := range names {
		fmt.Fprintf(stdout, "%s=%s\n", k, sh.vars[k])
	}
	return 0
}

func builtinSet(sh *Shell, args []string, _ io.Reader, stdout, _ io.Writer) int {
	if len(args) == 0 {
		names := make([]string, 0, len(sh.vars))
		for k := range sh.vars {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			fmt.Fprintf(stdout, "%s=%s\n", k, sh.vars[k])
		}
		return 0
	}
	// VAR=val pairs
	for _, a := range args {
		idx := strings.IndexByte(a, '=')
		if idx >= 0 {
			sh.setVar(a[:idx], a[idx+1:])
		}
	}
	return 0
}

func builtinSource(sh *Shell, args []string, _ io.Reader, _, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "source: filename required\n")
		return 1
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "source: %v\n", err)
		return 1
	}
	return sh.EvalString(string(data))
}

func builtinAlias(sh *Shell, args []string, _ io.Reader, stdout, _ io.Writer) int {
	if len(args) == 0 {
		names := make([]string, 0, len(sh.aliases))
		for k := range sh.aliases {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			fmt.Fprintf(stdout, "alias %s=%q\n", k, sh.aliases[k])
		}
		return 0
	}
	for _, a := range args {
		idx := strings.IndexByte(a, '=')
		if idx >= 0 {
			sh.aliases[a[:idx]] = a[idx+1:]
		} else {
			if v, ok := sh.aliases[a]; ok {
				fmt.Fprintf(stdout, "alias %s=%q\n", a, v)
			}
		}
	}
	return 0
}

func builtinUnalias(sh *Shell, args []string, _ io.Reader, _, _ io.Writer) int {
	for _, a := range args {
		delete(sh.aliases, a)
	}
	return 0
}

func builtinHistory(sh *Shell, _ []string, _ io.Reader, stdout, _ io.Writer) int {
	for i, h := range sh.History {
		fmt.Fprintf(stdout, "%5d  %s\n", i+1, h)
	}
	return 0
}

func builtinType(sh *Shell, args []string, _ io.Reader, stdout, stderr io.Writer) int {
	code := 0
	for _, name := range args {
		if _, ok := builtins[name]; ok {
			fmt.Fprintf(stdout, "%s is a shell builtin\n", name)
			continue
		}
		if v, ok := sh.aliases[name]; ok {
			fmt.Fprintf(stdout, "%s is aliased to %q\n", name, v)
			continue
		}
		if path, found := lookupCommand(name); found {
			fmt.Fprintf(stdout, "%s is %s\n", name, path)
			continue
		}
		fmt.Fprintf(stderr, "%s: type: %s: not found\n", sh.name, name)
		code = 1
	}
	return code
}

func builtinHelp(_ *Shell, _ []string, _ io.Reader, stdout, _ io.Writer) int {
	fmt.Fprintln(stdout, `posh — Portable Shell

Built-in commands:
  cd [dir]          Change directory (default: $HOME)
  pwd               Print working directory
  echo [-n] [args]  Print arguments
  printf fmt [args] Formatted output
  export [VAR[=val]]  Export variable to environment
  unset VAR...      Remove variable
  env               Print exported environment
  set [VAR=val]...  Set or list shell variables
  source FILE / . FILE  Execute FILE in current shell
  alias [name[=val]]  Define or list aliases
  unalias name...   Remove aliases
  history           Show command history
  type name...      Show type of name
  jobs              List background jobs
  true              Exit 0
  false             Exit 1
  :                 No-op (exit 0)
  help              Show this help
  exit [n]          Exit shell with status n`)
	return 0
}

func builtinJobs(sh *Shell, _ []string, _ io.Reader, stdout, _ io.Writer) int {
	for _, j := range sh.jobs.list() {
		fmt.Fprintf(stdout, "[%d] Running\t%s\n", j.ID, j.Desc)
	}
	return 0
}
