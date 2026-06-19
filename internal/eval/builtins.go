package eval

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// builtinFn is the signature for all built-in command implementations.
type builtinFn func(sh *Shell, args []string, stdin io.Reader, stdout, stderr io.Writer) int

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
		"clear":   builtinClear,
		"ls":      makeToolBuiltin("winls", "ls"),
		"wc":      makeToolBuiltin("winwc", "wc"),
		"which":   makeToolBuiltin("winwhich", "which"),
		"grep":    makeToolBuiltin("winegrep", "grep"),
		"egrep":   makeToolBuiltin("winegrep", "egrep"),
		"find":    makeToolBuiltin("winfind", "find"),
		"head":    makeToolBuiltin("winhead", "head"),
		"tail":    makeToolBuiltin("wintail", "tail"),
		"less":    makeToolBuiltin("winless", "less"),
		"true":    func(_ *Shell, _ []string, _ io.Reader, _, _ io.Writer) int { return 0 },
		"false":   func(_ *Shell, _ []string, _ io.Reader, _, _ io.Writer) int { return 1 },
		":":       func(_ *Shell, _ []string, _ io.Reader, _, _ io.Writer) int { return 0 },
		"jobs":    builtinJobs,
		"test":    builtinTest,
		"[":       builtinTestBracket,
		"break":   builtinBreak,
		"continue": builtinContinue,
		"return":  builtinReturn,
		"read":    builtinRead,
		"shift":   builtinShift,
		"trap":    builtinTrap,
		"fg":      builtinFg,
		"bg":      builtinBg,
		"wait":    builtinWait,
		"kill":    builtinKill,
	}
}

// ---- delegating tool builtins ----

// makeToolBuiltin returns a builtin that tries preferredName first (e.g. "winls"),
// then fallbackName (e.g. "ls"), running whichever is found in PATH.
// This lets ls/grep/wc/which/find/head/tail resolve to the win* equivalents when
// they are installed, while still working with any other tool on PATH.
func makeToolBuiltin(preferredName, fallbackName string) builtinFn {
	return func(sh *Shell, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
		for _, name := range []string{preferredName, fallbackName} {
			path, found := lookupCommand(name)
			if !found {
				continue
			}
			c := exec.Command(path, args...)
			c.Stdin = stdin
			c.Stdout = stdout
			c.Stderr = stderr
			if err := c.Run(); err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					return exitErr.ExitCode()
				}
				return 1
			}
			return 0
		}
		fmt.Fprintf(stderr, "%s: command not found\n", preferredName)
		return 127
	}
}

// ---- clear ----

func builtinClear(_ *Shell, _ []string, _ io.Reader, _ io.Writer, _ io.Writer) int {
	// cmd /c cls uses the Win32 Console API directly, which properly clears
	// the screen on both old Command Prompt and new Windows Terminal.
	// We bind it to os.Stdout (the real console handle) rather than our
	// wrapped writer, because cls writes via CONOUT$ internally.
	c := exec.Command("cmd", "/c", "cls")
	c.Stdout = os.Stdout
	if err := c.Run(); err != nil {
		// Fallback: ANSI escape for non-Windows or unusual setups
		fmt.Fprint(os.Stdout, "\033[2J\033[H")
	}
	return 0
}

// ---- cd, pwd ----

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

// ---- echo, printf ----

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
	format = strings.ReplaceAll(format, `\n`, "\n")
	format = strings.ReplaceAll(format, `\t`, "\t")
	format = strings.ReplaceAll(format, `\r`, "\r")
	fmt.Fprintf(stdout, format, fmtArgs...)
	return 0
}

// ---- exit ----

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

// ---- export, unset, env, set ----

func builtinExport(sh *Shell, args []string, _ io.Reader, stdout, _ io.Writer) int {
	if len(args) == 0 {
		names := make([]string, 0, len(sh.exported))
		for k := range sh.exported {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			fmt.Fprintf(stdout, "export %s=%q\n", k, sh.vars[k])
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

// knownOpts lists the shell options recognised by set -o / set +o.
var knownOpts = map[string]bool{
	"vi":       true,
	"emacs":    true,
	"errexit":  true,
	"nounset":  true,
	"noglob":   true,
	"pipefail": true,
	"xtrace":   true,
	"verbose":  true,
}

func builtinSet(sh *Shell, args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		// Print all shell variables
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

	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-o" || a == "+o":
			enable := a == "-o"
			i++
			if i >= len(args) {
				// No option name: print current option states
				optNames := make([]string, 0, len(knownOpts))
				for k := range knownOpts {
					optNames = append(optNames, k)
				}
				sort.Strings(optNames)
				for _, name := range optNames {
					state := "off"
					if sh.GetOpt(name) {
						state = "on"
					}
					fmt.Fprintf(stdout, "%-12s %s\n", name, state)
				}
				return 0
			}
			name := args[i]
			if !knownOpts[name] {
				fmt.Fprintf(stderr, "set: unknown option: %s\n", name)
				return 1
			}
			// vi and emacs are mutually exclusive editing modes
			if name == "vi" && enable {
				sh.SetOpt("emacs", false)
			} else if name == "emacs" && enable {
				sh.SetOpt("vi", false)
			}
			sh.SetOpt(name, enable)
		case strings.HasPrefix(a, "-o"):
			// -o<name> (no space)
			name := a[2:]
			if !knownOpts[name] {
				fmt.Fprintf(stderr, "set: unknown option: %s\n", name)
				return 1
			}
			if name == "vi" {
				sh.SetOpt("emacs", false)
			}
			sh.SetOpt(name, true)
		case strings.HasPrefix(a, "+o"):
			// +o<name> (no space)
			name := a[2:]
			if !knownOpts[name] {
				fmt.Fprintf(stderr, "set: unknown option: %s\n", name)
				return 1
			}
			sh.SetOpt(name, false)
		default:
			// VAR=val assignment
			idx := strings.IndexByte(a, '=')
			if idx >= 0 {
				sh.setVar(a[:idx], a[idx+1:])
			}
		}
		i++
	}
	return 0
}

// ---- source ----

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

// ---- alias, unalias ----

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

func builtinUnalias(sh *Shell, args []string, _ io.Reader, _, stderr io.Writer) int {
	for _, a := range args {
		if a == "-a" {
			sh.aliases = make(map[string]string)
			continue
		}
		delete(sh.aliases, a)
	}
	return 0
}

// ---- history, type, help ----

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
		if _, ok := sh.funcs[name]; ok {
			fmt.Fprintf(stdout, "%s is a shell function\n", name)
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
  cd [dir]            Change directory (default: $HOME)
  pwd                 Print working directory
  echo [-n] [args]    Print arguments
  printf fmt [args]   Formatted output
  export [VAR[=val]]  Export variable to environment
  unset VAR...        Remove variable
  env                 Print exported environment
  set [VAR=val]...    Set or list shell variables
  source FILE / . FILE  Execute FILE in current shell
  alias [name[=val]]  Define or list aliases
  unalias name...     Remove aliases
  history             Show command history
  type name...        Show type of name
  jobs [-l]           List background jobs (-l includes PIDs)
  fg [%n]             Bring job to foreground
  bg [%n]             Resume job in background
  wait [%n|pid]       Wait for job/process
  kill [-sig] pid|%n  Send signal to process or job (kill -l lists signals)
  test EXPR / [ EXPR ] Evaluate conditional expression
  break [n]           Break from n levels of loop
  continue [n]        Continue next iteration of loop
  return [n]          Return from function with exit code n
  read [-r] [-p P] VAR...  Read a line from stdin
  shift [n]           Shift positional parameters
  trap [cmd] [SIG]    Set signal handler
  true / false / :    Exit 0 / 1 / 0
  clear               Clear the screen
  ls                  List directory (uses winls if installed)
  wc                  Word/line/byte count (uses winwc if installed)
  which               Locate command (uses winwhich if installed)
  grep / egrep        Search files (uses winegrep if installed)
  find                Find files (uses winfind if installed)
  head / tail         Print first/last lines (uses winhead/wintail if installed)
  help                Show this help
  exit [n]            Exit shell

Control flow:
  if COND; then BODY; [elif COND; then BODY;]... [else BODY;] fi
  for VAR [in WORDS]; do BODY; done
  while COND; do BODY; done
  until COND; do BODY; done
  case WORD in PATTERN) BODY;; ... esac
  NAME() { BODY }
  function NAME { BODY }`)
	return 0
}

// ---- jobs, fg, bg, wait ----

func builtinJobs(sh *Shell, args []string, _ io.Reader, stdout, _ io.Writer) int {
	long := len(args) > 0 && args[0] == "-l"
	for _, j := range sh.jobs.list() {
		if long {
			fmt.Fprintf(stdout, "[%d] %d Running\t%s\n", j.ID, j.Cmd.Process.Pid, j.Desc)
		} else {
			fmt.Fprintf(stdout, "[%d] Running\t%s\n", j.ID, j.Desc)
		}
	}
	return 0
}

func builtinFg(sh *Shell, args []string, _ io.Reader, _, stderr io.Writer) int {
	jobs := sh.jobs.list()
	if len(jobs) == 0 {
		fmt.Fprintf(stderr, "%s: fg: no current job\n", sh.name)
		return 1
	}
	j := jobs[len(jobs)-1]
	if len(args) > 0 {
		id := parseJobID(args[0])
		for _, jj := range jobs {
			if jj.ID == id {
				j = jj
				break
			}
		}
	}
	fmt.Fprintf(os.Stderr, "%s\n", j.Desc)
	j.Cmd.Wait()
	return 0
}

func builtinBg(sh *Shell, args []string, _ io.Reader, _, stderr io.Writer) int {
	jobs := sh.jobs.list()
	if len(jobs) == 0 {
		fmt.Fprintf(stderr, "%s: bg: no current job\n", sh.name)
		return 1
	}
	j := jobs[len(jobs)-1]
	if len(args) > 0 {
		id := parseJobID(args[0])
		for _, jj := range jobs {
			if jj.ID == id {
				j = jj
				break
			}
		}
	}
	fmt.Fprintf(os.Stderr, "[%d] %s &\n", j.ID, j.Desc)
	return 0
}

func builtinWait(sh *Shell, args []string, _ io.Reader, _, _ io.Writer) int {
	if len(args) == 0 {
		for _, j := range sh.jobs.list() {
			j.Cmd.Wait()
		}
		return 0
	}
	id := parseJobID(args[0])
	for _, j := range sh.jobs.list() {
		if j.ID == id {
			j.Cmd.Wait()
			return 0
		}
	}
	return 0
}

// ---- kill ----

// killSignals maps signal names/numbers to the os.Signal to send on Windows.
// Windows only reliably delivers os.Kill (TerminateProcess) and os.Interrupt
// (Ctrl+C event); everything else is mapped to one of those two.
var killSignals = map[string]os.Signal{
	"1": os.Kill, "HUP": os.Kill,
	"2": os.Interrupt, "INT": os.Interrupt,
	"3": os.Kill, "QUIT": os.Kill,
	"6": os.Kill, "ABRT": os.Kill,
	"9": os.Kill, "KILL": os.Kill,
	"15": os.Kill, "TERM": os.Kill,
	"19": os.Kill, "STOP": os.Kill,
}

var killSignalList = [][2]string{
	{"1", "HUP"}, {"2", "INT"}, {"3", "QUIT"}, {"4", "ILL"},
	{"5", "TRAP"}, {"6", "ABRT"}, {"8", "FPE"}, {"9", "KILL"},
	{"11", "SEGV"}, {"13", "PIPE"}, {"14", "ALRM"}, {"15", "TERM"},
	{"17", "CHLD"}, {"18", "CONT"}, {"19", "STOP"}, {"20", "TSTP"},
	{"21", "TTIN"}, {"22", "TTOU"},
}

func builtinKill(sh *Shell, args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "usage: kill [-signal] pid|%%job ...\n")
		return 1
	}

	// -l: list signal names
	if args[0] == "-l" {
		for _, pair := range killSignalList {
			fmt.Fprintf(stdout, "%s) SIG%s\n", pair[0], pair[1])
		}
		return 0
	}

	// Parse optional leading signal flag: -N  -SIGNAME  -NAME
	sig := os.Kill // default is SIGTERM → mapped to Kill on Windows
	targets := args
	if strings.HasPrefix(args[0], "-") {
		name := strings.ToUpper(strings.TrimPrefix(args[0], "-"))
		name = strings.TrimPrefix(name, "SIG")
		s, ok := killSignals[name]
		if !ok {
			fmt.Fprintf(stderr, "%s: kill: unknown signal %s\n", sh.name, args[0])
			return 1
		}
		sig = s
		targets = args[1:]
	}

	if len(targets) == 0 {
		fmt.Fprintf(stderr, "usage: kill [-signal] pid|%%job ...\n")
		return 1
	}

	code := 0
	for _, t := range targets {
		if strings.HasPrefix(t, "%") {
			id := parseJobID(t)
			found := false
			for _, j := range sh.jobs.list() {
				if j.ID == id {
					found = true
					if err := j.Cmd.Process.Signal(sig); err != nil {
						fmt.Fprintf(stderr, "%s: kill: %%%d: %v\n", sh.name, id, err)
						code = 1
					}
					break
				}
			}
			if !found {
				fmt.Fprintf(stderr, "%s: kill: %s: no such job\n", sh.name, t)
				code = 1
			}
		} else {
			pid, err := strconv.Atoi(t)
			if err != nil || pid <= 0 {
				fmt.Fprintf(stderr, "%s: kill: %s: invalid pid\n", sh.name, t)
				code = 1
				continue
			}
			proc, err := os.FindProcess(pid)
			if err != nil {
				fmt.Fprintf(stderr, "%s: kill: %d: %v\n", sh.name, pid, err)
				code = 1
				continue
			}
			if err := proc.Signal(sig); err != nil {
				fmt.Fprintf(stderr, "%s: kill: %d: %v\n", sh.name, pid, err)
				code = 1
			}
		}
	}
	return code
}

func parseJobID(s string) int {
	s = strings.TrimPrefix(s, "%")
	n, _ := strconv.Atoi(s)
	return n
}

// ---- control flow builtins ----

func builtinBreak(_ *Shell, args []string, _ io.Reader, _, _ io.Writer) int {
	n := 1
	if len(args) > 0 {
		if v, err := strconv.Atoi(args[0]); err == nil && v > 0 {
			n = v
		}
	}
	panic(loopBreak{n})
}

func builtinContinue(_ *Shell, args []string, _ io.Reader, _, _ io.Writer) int {
	n := 1
	if len(args) > 0 {
		if v, err := strconv.Atoi(args[0]); err == nil && v > 0 {
			n = v
		}
	}
	panic(loopContinue{n})
}

func builtinReturn(sh *Shell, args []string, _ io.Reader, _, _ io.Writer) int {
	code := sh.lastExit
	if len(args) > 0 {
		if v, err := strconv.Atoi(args[0]); err == nil {
			code = v
		}
	}
	panic(funcReturn{code})
}

// ---- read ----

func builtinRead(sh *Shell, args []string, stdin io.Reader, stdout, _ io.Writer) int {
	rawMode := false
	prompt := ""
	var varNames []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-r":
			rawMode = true
		case "-p":
			i++
			if i < len(args) {
				prompt = args[i]
			}
		default:
			varNames = append(varNames, args[i])
		}
	}

	if prompt != "" {
		fmt.Fprint(stdout, prompt)
	}

	reader := bufio.NewReader(stdin)
	line, err := reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return 1
	}
	line = strings.TrimRight(line, "\r\n")

	if !rawMode {
		// Handle backslash-newline continuation (shouldn't appear at this level but be safe)
		line = strings.ReplaceAll(line, "\\\n", "")
	}

	if len(varNames) == 0 {
		sh.setVar("REPLY", line)
		return 0
	}

	ifs := sh.getVar("IFS")
	if ifs == "" {
		ifs = " \t"
	}

	parts := splitByIFS(line, ifs)
	for i, name := range varNames {
		if i < len(parts) {
			if i == len(varNames)-1 {
				// Last variable gets remaining parts joined
				sh.setVar(name, strings.Join(parts[i:], " "))
			} else {
				sh.setVar(name, parts[i])
			}
		} else {
			sh.setVar(name, "")
		}
	}
	return 0
}

func splitByIFS(s, ifs string) []string {
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
	return parts
}

// ---- shift ----

func builtinShift(sh *Shell, args []string, _ io.Reader, _, stderr io.Writer) int {
	n := 1
	if len(args) > 0 {
		if v, err := strconv.Atoi(args[0]); err == nil {
			n = v
		}
	}
	if n > len(sh.posParams) {
		n = len(sh.posParams)
	}
	sh.posParams = sh.posParams[n:]
	return 0
}

// ---- trap ----

func builtinTrap(sh *Shell, args []string, _ io.Reader, stdout, _ io.Writer) int {
	if len(args) == 0 {
		// List traps
		sigs := make([]string, 0, len(sh.traps))
		for k := range sh.traps {
			sigs = append(sigs, k)
		}
		sort.Strings(sigs)
		for _, sig := range sigs {
			fmt.Fprintf(stdout, "trap -- %q %s\n", sh.traps[sig], sig)
		}
		return 0
	}
	if len(args) == 1 {
		return 0
	}
	cmd := args[0]
	for _, sig := range args[1:] {
		sig = strings.ToUpper(sig)
		if cmd == "-" {
			delete(sh.traps, sig)
		} else {
			sh.traps[sig] = cmd
		}
	}
	return 0
}

// ---- test / [ ----

func builtinTest(_ *Shell, args []string, _ io.Reader, _, _ io.Writer) int {
	result, _ := evalTest(args)
	if result {
		return 0
	}
	return 1
}

func builtinTestBracket(_ *Shell, args []string, _ io.Reader, _, stderr io.Writer) int {
	if len(args) == 0 || args[len(args)-1] != "]" {
		fmt.Fprintln(stderr, "[: missing ']'")
		return 2
	}
	args = args[:len(args)-1]
	result, _ := evalTest(args)
	if result {
		return 0
	}
	return 1
}

// evalTest evaluates POSIX test expressions.
func evalTest(args []string) (bool, int) {
	if len(args) == 0 {
		return false, 0
	}

	// Logical not
	if args[0] == "!" {
		v, n := evalTest(args[1:])
		return !v, n + 1
	}

	// Compound: -a and -o
	// Find lowest-precedence -o first
	for i := 1; i < len(args)-1; i++ {
		if args[i] == "-o" {
			left, _ := evalTest(args[:i])
			right, _ := evalTest(args[i+1:])
			return left || right, len(args)
		}
	}
	for i := 1; i < len(args)-1; i++ {
		if args[i] == "-a" {
			left, _ := evalTest(args[:i])
			right, _ := evalTest(args[i+1:])
			return left && right, len(args)
		}
	}

	// Unary tests
	if len(args) == 2 {
		op, arg := args[0], args[1]
		switch op {
		case "-z":
			return len(arg) == 0, 2
		case "-n":
			return len(arg) != 0, 2
		case "-f":
			fi, err := os.Stat(arg)
			return err == nil && fi.Mode().IsRegular(), 2
		case "-d":
			fi, err := os.Stat(arg)
			return err == nil && fi.IsDir(), 2
		case "-e":
			_, err := os.Stat(arg)
			return err == nil, 2
		case "-r":
			f, err := os.OpenFile(arg, os.O_RDONLY, 0)
			if err != nil {
				return false, 2
			}
			f.Close()
			return true, 2
		case "-w":
			f, err := os.OpenFile(arg, os.O_WRONLY, 0)
			if err != nil {
				return false, 2
			}
			f.Close()
			return true, 2
		case "-x":
			fi, err := os.Stat(arg)
			return err == nil && !fi.IsDir(), 2 // Windows: any file is "executable"
		case "-s":
			fi, err := os.Stat(arg)
			return err == nil && fi.Size() > 0, 2
		case "-L", "-h":
			fi, err := os.Lstat(arg)
			return err == nil && fi.Mode()&os.ModeSymlink != 0, 2
		}
	}

	// Binary tests
	if len(args) == 3 {
		left, op, right := args[0], args[1], args[2]
		switch op {
		case "=", "==":
			return left == right, 3
		case "!=":
			return left != right, 3
		case "<":
			return left < right, 3
		case ">":
			return left > right, 3
		case "-eq":
			return cmpInt(left, right) == 0, 3
		case "-ne":
			return cmpInt(left, right) != 0, 3
		case "-lt":
			return cmpInt(left, right) < 0, 3
		case "-le":
			return cmpInt(left, right) <= 0, 3
		case "-gt":
			return cmpInt(left, right) > 0, 3
		case "-ge":
			return cmpInt(left, right) >= 0, 3
		}
	}

	// Single string (non-empty = true)
	if len(args) == 1 {
		return len(args[0]) > 0, 1
	}

	return false, 0
}

func cmpInt(a, b string) int {
	ia, _ := strconv.ParseInt(strings.TrimSpace(a), 10, 64)
	ib, _ := strconv.ParseInt(strings.TrimSpace(b), 10, 64)
	if ia < ib {
		return -1
	}
	if ia > ib {
		return 1
	}
	return 0
}
