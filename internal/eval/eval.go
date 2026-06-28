// Package eval executes a posh AST.
package eval

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/fermat-tech/posh/internal/parser"
)

// ---- control-flow signals (panic/recover) ----

type loopBreak struct{ levels int }
type loopContinue struct{ levels int }
type funcReturn struct{ code int }

// shellExit is raised by the `exit` builtin. It unwinds the current shell
// execution context. Nested contexts that represent a separate "shell" — a
// subshell ( ... ), a command substitution, a background/pipeline goroutine, or
// a posh script run as a child — contain it via catchExit and report it as an
// ordinary exit status. At the top level it propagates out so the session ends.
type shellExit struct{ code int }

// catchExit runs fn and converts a shellExit panic into a returned exit code.
// It is used at every boundary where `exit` must terminate only the nested
// context rather than the whole process. Other panics propagate unchanged.
func catchExit(fn func() int) (code int) {
	defer func() {
		if r := recover(); r != nil {
			if ex, ok := r.(shellExit); ok {
				code = ex.code
				return
			}
			panic(r)
		}
	}()
	return fn()
}

// ExitCode reports whether a recovered panic value is a shell `exit` request
// and, if so, the status code to exit with. The process owner (main and the
// REPL loop) calls this from a top-level recover so that `exit` ends the
// session cleanly — letting deferred cleanup such as history saving run during
// unwinding — instead of os.Exit firing from deep inside the evaluator.
func ExitCode(r any) (code int, ok bool) {
	if ex, isExit := r.(shellExit); isExit {
		return ex.code, true
	}
	return 0, false
}

// Shell holds the execution state for a posh session.
type Shell struct {
	name      string                       // argv[0] / $0
	vars      map[string]string            // scalar shell variables
	arrays    map[string][]string          // indexed array variables
	assoc     map[string]map[string]string // associative (string-keyed) arrays
	exported  map[string]bool              // names exported to child processes
	aliases   map[string]string
	funcs     map[string]*parser.FuncDef
	posParams []string // $1 $2 ... (set for scripts/functions)
	lastExit  int      // $?
	parseErr  bool     // true when the last EvalStringAt call had a lex/parse error
	jobs      *JobTable
	traps     map[string]string // signal name → command
	opts         map[string]bool   // shell options: set -o name / set +o name
	isBackground bool              // true when running as a background job goroutine

	// I/O streams for this shell instance
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	History []string
}

// New creates a Shell with the given name and inherits the OS environment.
func New(name string) *Shell {
	sh := &Shell{
		name:     name,
		vars:     make(map[string]string),
		arrays:   make(map[string][]string),
		assoc:    make(map[string]map[string]string),
		exported: make(map[string]bool),
		aliases:  make(map[string]string),
		funcs:    make(map[string]*parser.FuncDef),
		jobs:     newJobTable(),
		traps:    make(map[string]string),
		opts:     make(map[string]bool),
		Stdin:    os.Stdin,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
	}
	for _, kv := range os.Environ() {
		idx := strings.IndexByte(kv, '=')
		if idx > 0 {
			k, v := kv[:idx], kv[idx+1:]
			sh.vars[k] = v
			sh.exported[k] = true
		}
	}
	// On Windows, HOME is not a standard env var; synthesize it from USERPROFILE.
	if sh.vars["HOME"] == "" {
		if up := sh.vars["USERPROFILE"]; up != "" {
			sh.vars["HOME"] = up
			sh.exported["HOME"] = true
		}
	}
	return sh
}

// GetVar is the exported accessor for use outside the package.
func (sh *Shell) GetVar(name string) string { return sh.getVar(name) }

// ExpandWord is the exported version of expandWord, used by tab completion.
func (sh *Shell) ExpandWord(w string) string { return sh.expandWord(w) }

// Vars returns a snapshot of all shell variable names (for tab completion).
func (sh *Shell) Vars() map[string]string {
	out := make(map[string]string, len(sh.vars))
	for k, v := range sh.vars {
		out[k] = v
	}
	return out
}
func (sh *Shell) GetOpt(name string) bool   { return sh.opts[name] }
func (sh *Shell) SetOpt(name string, val bool) {
	if val {
		sh.opts[name] = true
	} else {
		delete(sh.opts, name)
	}
}

func (sh *Shell) getVar(name string) string {
	if v, ok := sh.vars[name]; ok {
		return v
	}
	// Referencing an array by name yields its first element (bash semantics).
	if arr, ok := sh.arrays[name]; ok && len(arr) > 0 {
		return arr[0]
	}
	return ""
}

func (sh *Shell) setVar(name, val string) {
	sh.vars[name] = val
}

// SetPosParams sets $1, $2, ... from the given slice.
func (sh *Shell) SetPosParams(params []string) {
	sh.posParams = params
}

// inlineEnv applies command-prefix assignments (e.g. `TZ= date`) on top of base,
// returning the environment for the child process. Each assignment OVERRIDES any
// existing entry for the same name rather than appending a duplicate — otherwise
// the child's getenv would resolve to the original value and ignore the prefix.
// Only scalar VAR=val and VAR+=val forms are exported; array assignments are
// skipped (a process environment has no array values).
func (sh *Shell) inlineEnv(base, assigns []string) []string {
	if len(assigns) == 0 {
		return base
	}
	env := append([]string(nil), base...)
	pos := make(map[string]int, len(env))
	for i, kv := range env {
		if eq := strings.IndexByte(kv, '='); eq > 0 {
			pos[kv[:eq]] = i
		}
	}
	for _, a := range assigns {
		ap, ok := parseAssignParts(a)
		if !ok || ap.hasSub || isArrayLiteral(ap.value) {
			continue
		}
		val := unprotectWord(sh.expandWord(ap.value))
		if ap.append {
			if i, ok := pos[ap.name]; ok {
				val = env[i][len(ap.name)+1:] + val
			}
		}
		entry := ap.name + "=" + val
		if i, ok := pos[ap.name]; ok {
			env[i] = entry
		} else {
			pos[ap.name] = len(env)
			env = append(env, entry)
		}
	}
	return env
}

func (sh *Shell) exportedEnv() []string {
	var env []string
	for k, v := range sh.vars {
		if sh.exported[k] {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// Eval executes an AST node and returns the exit code.
func (sh *Shell) Eval(node parser.Node) int {
	if node == nil {
		return 0
	}
	switch n := node.(type) {
	case *parser.List:
		return sh.evalList(n)
	case *parser.Pipeline:
		return sh.evalPipeline(n)
	case *parser.Subshell:
		return sh.evalSubshell(n)
	case *parser.GroupCmd:
		return sh.evalGroupCmd(n)
	case *parser.SimpleCmd:
		return sh.evalSimpleCmd(n, sh.Stdin, sh.Stdout, sh.Stderr)
	case *parser.IfCmd:
		return sh.evalIfCmd(n)
	case *parser.ForCmd:
		return sh.evalForCmd(n)
	case *parser.WhileCmd:
		return sh.evalWhileCmd(n)
	case *parser.CaseCmd:
		return sh.evalCaseCmd(n)
	case *parser.FuncDef:
		return sh.evalFuncDef(n)
	case *parser.ArithCmd:
		val := evalArith(sh, n.Expr)
		if val != 0 {
			sh.lastExit = 0
			return 0
		}
		sh.lastExit = 1
		return 1
	}
	return 0
}

// EvalString parses and evaluates a string.
func (sh *Shell) EvalString(s string) int {
	return sh.EvalStringAt(s, 1)
}

// EvalStringAt parses and evaluates s, reporting errors with lineBase as the first line number.
func (sh *Shell) EvalStringAt(s string, lineBase int) int {
	s = preprocessHeredocs(s)
	node, err := parser.ParseAt(s, lineBase)
	if err != nil {
		fmt.Fprintf(sh.Stderr, "%s: %v\n", sh.name, err)
		sh.lastExit = 1
		sh.parseErr = true
		return 1
	}
	sh.parseErr = false
	code := sh.Eval(node)
	sh.lastExit = code
	return code
}

// LastExit returns the exit code of the last evaluated command.
func (sh *Shell) LastExit() int { return sh.lastExit }

// HasParseError reports whether the last EvalStringAt call failed due to a lex/parse error.
func (sh *Shell) HasParseError() bool { return sh.parseErr }

func (sh *Shell) evalList(list *parser.List) int {
	// ampAt reports whether the separator at index i is &, meaning the node
	// immediately before it should run in the background.
	ampAt := func(i int) bool {
		return i < len(list.Elems) && list.Elems[i].Op == parser.OpAmp
	}

	// list.First runs in background when the first separator is &.
	bg := ampAt(0)
	code := sh.evalNode(list.First, bg)
	if bg {
		code = 0
	}
	// Keep $? current after each statement so later commands in the same list
	// (e.g. echo "$?") observe the right status. Compound commands such as a
	// bare subshell don't route through evalPipeline, which is the other place
	// lastExit is maintained.
	sh.lastExit = code

	for i, elem := range list.Elems {
		if elem.Node == nil {
			continue
		}
		// elem.Node runs in background when the *next* separator is &.
		bg = ampAt(i + 1)
		switch elem.Op {
		case parser.OpSemi, parser.OpAmp:
			code = sh.evalNode(elem.Node, bg)
		case parser.OpAnd:
			if code == 0 {
				code = sh.evalNode(elem.Node, bg)
			}
		case parser.OpOr:
			if code != 0 {
				code = sh.evalNode(elem.Node, bg)
			}
		}
		if bg {
			code = 0
		}
		sh.lastExit = code
	}
	return code
}

func (sh *Shell) evalNode(n parser.Node, background bool) int {
	if n == nil {
		return 0
	}
	if background {
		child := sh.fork()
		child.isBackground = true
		// `exit` in a background job ends that job only.
		go catchExit(func() int { return child.Eval(n) })
		return 0
	}
	return sh.Eval(n)
}

func (sh *Shell) evalPipeline(pipe *parser.Pipeline) int {
	if len(pipe.Cmds) == 1 {
		code := sh.evalNodeIO(pipe.Cmds[0], sh.Stdin, sh.Stdout, sh.Stderr)
		if pipe.Negate {
			if code == 0 {
				return 1
			}
			return 0
		}
		sh.lastExit = code
		return code
	}

	n := len(pipe.Cmds)
	pipes := make([]*io.PipeReader, n-1)
	pipeW := make([]*io.PipeWriter, n-1)
	for i := range pipes {
		pipes[i], pipeW[i] = io.Pipe()
	}

	type result struct{ code int }
	results := make([]chan result, n)
	for i := range results {
		results[i] = make(chan result, 1)
	}

	for i, cmd := range pipe.Cmds {
		var stdin io.Reader = sh.Stdin
		var stdout io.Writer = sh.Stdout
		if i > 0 {
			stdin = pipes[i-1]
		}
		if i < n-1 {
			stdout = pipeW[i]
		}
		go func(idx int, node parser.Node, in io.Reader, out io.Writer) {
			sub := sh.fork()
			sub.Stdin, sub.Stdout, sub.Stderr = in, out, sh.Stderr
			// Each pipeline stage is its own subshell, so `exit` ends only
			// that stage (and must not escape the goroutine as a panic).
			code := catchExit(func() int {
				return sub.evalNodeIO(node, in, out, sh.Stderr)
			})
			// Close the write end so the next stage sees EOF.
			if idx < n-1 {
				pipeW[idx].Close()
			}
			// Close the read end so the previous stage gets a broken-pipe
			// error and exits instead of blocking on a full pipe buffer.
			if idx > 0 {
				pipes[idx-1].Close()
			}
			results[idx] <- result{code}
		}(i, cmd, stdin, stdout)
	}

	var lastCode int
	for i, ch := range results {
		r := <-ch
		if i == n-1 {
			lastCode = r.code
		}
	}

	if pipe.Negate {
		if lastCode == 0 {
			return 1
		}
		return 0
	}
	sh.lastExit = lastCode
	return lastCode
}

// evalNodeIO evaluates a node with explicit I/O, running in the current shell context.
func (sh *Shell) evalNodeIO(n parser.Node, stdin io.Reader, stdout, stderr io.Writer) int {
	switch v := n.(type) {
	case *parser.SimpleCmd:
		return sh.evalSimpleCmd(v, stdin, stdout, stderr)
	default:
		// Compound commands: temporarily redirect I/O
		old := [3]interface{}{sh.Stdin, sh.Stdout, sh.Stderr}
		sh.Stdin, sh.Stdout, sh.Stderr = stdin, stdout, stderr
		code := sh.Eval(n)
		sh.Stdin, sh.Stdout, sh.Stderr = old[0].(io.Reader), old[1].(io.Writer), old[2].(io.Writer)
		return code
	}
}

func (sh *Shell) evalSubshell(sub *parser.Subshell) int {
	child := sh.fork()
	var code int
	if sub.Body != nil {
		rIn, rOut, rErr, cleanup, err := applyRedirs(sh, sub.Redirs, sh.Stdin, sh.Stdout, sh.Stderr)
		if err != nil {
			fmt.Fprintf(sh.Stderr, "%s: %v\n", sh.name, err)
			return 1
		}
		defer cleanup()
		child.Stdin, child.Stdout, child.Stderr = rIn, rOut, rErr
		// `exit` inside ( ... ) exits only the subshell.
		code = catchExit(func() int { return child.evalList(sub.Body) })
	}
	return code
}

func (sh *Shell) evalGroupCmd(grp *parser.GroupCmd) int {
	rIn, rOut, rErr, cleanup, err := applyRedirs(sh, grp.Redirs, sh.Stdin, sh.Stdout, sh.Stderr)
	if err != nil {
		fmt.Fprintf(sh.Stderr, "%s: %v\n", sh.name, err)
		return 1
	}
	defer cleanup()

	old := [3]interface{}{sh.Stdin, sh.Stdout, sh.Stderr}
	sh.Stdin, sh.Stdout, sh.Stderr = rIn, rOut, rErr
	var code int
	if grp.Body != nil {
		code = sh.evalList(grp.Body)
	}
	sh.Stdin, sh.Stdout, sh.Stderr = old[0].(io.Reader), old[1].(io.Writer), old[2].(io.Writer)
	return code
}

// ---- compound command evaluators ----

func (sh *Shell) evalIfCmd(cmd *parser.IfCmd) int {
	rIn, rOut, rErr, cleanup, err := applyRedirs(sh, cmd.Redirs, sh.Stdin, sh.Stdout, sh.Stderr)
	if err != nil {
		fmt.Fprintf(sh.Stderr, "%s: %v\n", sh.name, err)
		return 1
	}
	defer cleanup()
	old := [3]interface{}{sh.Stdin, sh.Stdout, sh.Stderr}
	sh.Stdin, sh.Stdout, sh.Stderr = rIn, rOut, rErr

	var code int
	if sh.evalList(cmd.Cond) == 0 {
		if cmd.Then != nil {
			code = sh.evalList(cmd.Then)
		}
	} else {
		handled := false
		for _, elif := range cmd.Elifs {
			if sh.evalList(elif.Cond) == 0 {
				if elif.Then != nil {
					code = sh.evalList(elif.Then)
				}
				handled = true
				break
			}
		}
		if !handled && cmd.Else != nil {
			code = sh.evalList(cmd.Else)
		}
	}

	sh.Stdin, sh.Stdout, sh.Stderr = old[0].(io.Reader), old[1].(io.Writer), old[2].(io.Writer)
	return code
}

func (sh *Shell) evalForCmd(cmd *parser.ForCmd) (code int) {
	rIn, rOut, rErr, cleanup, err := applyRedirs(sh, cmd.Redirs, sh.Stdin, sh.Stdout, sh.Stderr)
	if err != nil {
		fmt.Fprintf(sh.Stderr, "%s: %v\n", sh.name, err)
		return 1
	}
	defer cleanup()
	old := [3]interface{}{sh.Stdin, sh.Stdout, sh.Stderr}
	sh.Stdin, sh.Stdout, sh.Stderr = rIn, rOut, rErr
	defer func() { sh.Stdin, sh.Stdout, sh.Stderr = old[0].(io.Reader), old[1].(io.Writer), old[2].(io.Writer) }()

	var values []string
	if cmd.Words == nil {
		values = sh.posParams
	} else {
		for _, w := range cmd.Words {
			expanded := sh.expandWords([]string{w})
			values = append(values, expanded...)
		}
	}

	for _, val := range values {
		sh.setVar(cmd.Var, val)
		var shouldBreak bool
		func() {
			defer func() {
				if r := recover(); r != nil {
					switch v := r.(type) {
					case loopBreak:
						if v.levels > 1 {
							panic(loopBreak{v.levels - 1})
						}
						shouldBreak = true
					case loopContinue:
						if v.levels > 1 {
							panic(loopContinue{v.levels - 1})
						}
						// levels==1: caught, continue to next iteration
					default:
						panic(r)
					}
				}
			}()
			if cmd.Body != nil {
				code = sh.evalList(cmd.Body)
			}
		}()
		if shouldBreak {
			break
		}
	}
	return code
}

func (sh *Shell) evalWhileCmd(cmd *parser.WhileCmd) (code int) {
	rIn, rOut, rErr, cleanup, err := applyRedirs(sh, cmd.Redirs, sh.Stdin, sh.Stdout, sh.Stderr)
	if err != nil {
		fmt.Fprintf(sh.Stderr, "%s: %v\n", sh.name, err)
		return 1
	}
	defer cleanup()
	old := [3]interface{}{sh.Stdin, sh.Stdout, sh.Stderr}
	sh.Stdin, sh.Stdout, sh.Stderr = rIn, rOut, rErr
	defer func() { sh.Stdin, sh.Stdout, sh.Stderr = old[0].(io.Reader), old[1].(io.Writer), old[2].(io.Writer) }()

	for {
		condCode := 0
		if cmd.Cond != nil {
			condCode = sh.evalList(cmd.Cond)
		}
		// while: continue when condCode==0; until: continue when condCode!=0
		if cmd.Until {
			if condCode == 0 {
				break
			}
		} else {
			if condCode != 0 {
				break
			}
		}

		var shouldBreak bool
		func() {
			defer func() {
				if r := recover(); r != nil {
					switch v := r.(type) {
					case loopBreak:
						if v.levels > 1 {
							panic(loopBreak{v.levels - 1})
						}
						shouldBreak = true
					case loopContinue:
						if v.levels > 1 {
							panic(loopContinue{v.levels - 1})
						}
					default:
						panic(r)
					}
				}
			}()
			if cmd.Body != nil {
				code = sh.evalList(cmd.Body)
			}
		}()
		if shouldBreak {
			break
		}
	}
	return code
}

func (sh *Shell) evalCaseCmd(cmd *parser.CaseCmd) int {
	rIn, rOut, rErr, cleanup, err := applyRedirs(sh, cmd.Redirs, sh.Stdin, sh.Stdout, sh.Stderr)
	if err != nil {
		fmt.Fprintf(sh.Stderr, "%s: %v\n", sh.name, err)
		return 1
	}
	defer cleanup()
	old := [3]interface{}{sh.Stdin, sh.Stdout, sh.Stderr}
	sh.Stdin, sh.Stdout, sh.Stderr = rIn, rOut, rErr
	defer func() { sh.Stdin, sh.Stdout, sh.Stderr = old[0].(io.Reader), old[1].(io.Writer), old[2].(io.Writer) }()

	word := sh.expandWord(cmd.Word)
	for _, clause := range cmd.Clauses {
		for _, pat := range clause.Patterns {
			if matchCasePattern(pat, word) {
				if clause.Body != nil {
					return sh.evalList(clause.Body)
				}
				return 0
			}
		}
	}
	return 0
}

func matchCasePattern(pattern, word string) bool {
	if pattern == "*" {
		return true
	}
	matched, err := filepath.Match(pattern, word)
	return err == nil && matched
}

func (sh *Shell) evalFuncDef(def *parser.FuncDef) int {
	sh.funcs[def.Name] = def
	return 0
}

func (sh *Shell) callFunc(def *parser.FuncDef, args []string, assigns ...string) (code int) {
	child := sh.fork()
	child.posParams = args
	// Command-prefix assignments (e.g. `VAR=val func`) apply to the function call
	// and are exported so any commands it runs inherit them.
	for _, a := range assigns {
		child.applyAssign(a)
		if ap, ok := parseAssignParts(a); ok && !ap.hasSub {
			child.exported[ap.name] = true
		}
	}

	defer func() {
		if r := recover(); r != nil {
			if ret, ok := r.(funcReturn); ok {
				code = ret.code
				return
			}
			panic(r)
		}
	}()
	code = child.Eval(def.Body)
	sh.lastExit = code
	return code
}

// ---- simple command ----

func (sh *Shell) evalSimpleCmd(cmd *parser.SimpleCmd, stdin io.Reader, stdout, stderr io.Writer) int {
	// Inline assignments with no command
	if len(cmd.Words) == 0 {
		for _, a := range cmd.Assigns {
			sh.applyAssign(a)
		}
		return 0
	}

	// Apply redirections
	rStdin, rStdout, rStderr, cleanup, err := applyRedirs(sh, cmd.Redirs, stdin, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", sh.name, err)
		return 1
	}
	defer cleanup()

	// Expand command words
	words := sh.expandWords(cmd.Words)
	if len(words) == 0 {
		return 0
	}

	// Build the inline environment from the exported variables plus the
	// command-prefix assignments.
	cmdEnv := sh.inlineEnv(sh.exportedEnv(), cmd.Assigns)

	name := words[0]

	// Alias expansion
	if expanded, ok := sh.aliases[name]; ok {
		full := expanded
		if len(words) > 1 {
			full += " " + strings.Join(words[1:], " ")
		}
		node, perr := parser.Parse(full)
		if perr == nil && node != nil {
			sub := sh.fork()
			sub.Stdin, sub.Stdout, sub.Stderr = rStdin, rStdout, rStderr
			// Carry any command-prefix assignments (e.g. `TZ=UTC date`) into the
			// alias's environment so they reach the command it expands to, and
			// export them so they reach child processes.
			for _, a := range cmd.Assigns {
				sub.applyAssign(a)
				if ap, ok := parseAssignParts(a); ok && !ap.hasSub {
					sub.exported[ap.name] = true
				}
			}
			code := sub.Eval(node)
			sh.lastExit = code
			return code
		}
	}

	// Built-in check
	if bi, ok := builtins[name]; ok {
		code := bi(sh, words[1:], rStdin, rStdout, rStderr)
		sh.lastExit = code
		return code
	}

	// Shell function check
	if fn, ok := sh.funcs[name]; ok {
		code := sh.callFunc(fn, words[1:], cmd.Assigns...)
		sh.lastExit = code
		return code
	}

	// External command
	resolvedPath, found := lookupCommand(name)
	if !found {
		fmt.Fprintf(rStderr, "%s: %s: command not found\n", sh.name, name)
		sh.lastExit = 127
		return 127
	}

	c := exec.Command(resolvedPath, words[1:]...)
	c.Env = cmdEnv
	c.Stdin = rStdin
	c.Stdout = rStdout
	c.Stderr = rStderr

	if sh.isBackground {
		// Detach stdin so the background process cannot consume key events
		// from the console input buffer while the shell is reading input.
		if devNull, err := os.Open(os.DevNull); err == nil {
			c.Stdin = devNull
			defer devNull.Close()
		}
		// Use raw OS file handles for stdout/stderr.  If we leave a wrapped
		// writer (e.g. colorable) Go's exec creates internal copy goroutines,
		// and cmd.Wait() blocks until every process holding the write-end of
		// that pipe closes it — including orphaned grandchildren.  With a real
		// *os.File the handle is inherited directly and Wait() unblocks as
		// soon as the tracked process exits.
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		// Own process group: a Ctrl+C meant for the foreground command must not
		// reach background jobs (matches bash job-control behavior).
		setBackgroundAttrs(c)
		if err := c.Start(); err != nil {
			fmt.Fprintf(rStderr, "%s: %v\n", sh.name, err)
			return 1
		}
		sh.jobs.add(c, strings.Join(words, " "))
		return 0
	}

	// Put the child in its own process group and capture Ctrl+C in posh.
	// Windows does not forward CTRL_C_EVENT to child processes automatically,
	// so we catch it here and send CTRL_BREAK_EVENT to the child's group.
	setForegroundAttrs(c)
	if err := c.Start(); err != nil {
		// On Unix, exec fails with ENOENT when the shebang interpreter isn't
		// found (e.g. CRLF-corrupted "#!/usr/bin/env posh\r"), and with
		// ENOEXEC when the file has no shebang at all.  In both cases, fall
		// back to interpreting the file as a posh script.
		if code := sh.tryRunAsScript(resolvedPath, words[1:], rStdin, rStdout, rStderr); code >= 0 {
			sh.lastExit = code
			return code
		}
		fmt.Fprintf(rStderr, "%s: %v\n", sh.name, err)
		return 1
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	cmdDone := make(chan struct{})
	go func() {
		select {
		case <-sigCh:
			sendInterrupt(c.Process.Pid)
		case <-cmdDone:
		}
	}()

	runErr := c.Wait()
	close(cmdDone)
	signal.Stop(sigCh)
	select {
	case <-sigCh:
	default:
	}

	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			sh.lastExit = code
			return code
		}
		fmt.Fprintf(rStderr, "%s: %v\n", sh.name, runErr)
		sh.lastExit = 1
		return 1
	}
	sh.lastExit = 0
	return 0
}

// runCaptured runs a shell string and captures its stdout.
// tryRunAsScript attempts to interpret path as a posh script when exec failed.
// Returns the exit code on success, or -1 if the file cannot be read or is
// clearly not a text script (e.g. a real binary).
func (sh *Shell) tryRunAsScript(path string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	// Reject obvious binaries (ELF, PE, Mach-O magic bytes).
	if len(data) >= 4 &&
		(data[0] == 0x7f && data[1] == 'E' || // ELF
			data[0] == 'M' && data[1] == 'Z' || // PE/DOS
			data[0] == 0xfe && data[1] == 0xed || // Mach-O BE
			data[0] == 0xce && data[1] == 0xfa) { // Mach-O LE
		return -1
	}
	// Normalise CRLF → LF so Windows-edited scripts work on Unix.
	src := strings.ReplaceAll(string(data), "\r\n", "\n")
	// Skip shebang line if present.
	if strings.HasPrefix(src, "#!") {
		if nl := strings.Index(src, "\n"); nl >= 0 {
			src = src[nl+1:]
		} else {
			src = ""
		}
	}
	sub := sh.fork()
	sub.Stdin = stdin
	sub.Stdout = stdout
	sub.Stderr = stderr
	sub.SetPosParams(args)
	// A script runs as a child: its `exit` returns a status to us, it does not
	// terminate the interactive session that launched it.
	return catchExit(func() int { return sub.EvalString(src) })
}

func (sh *Shell) runCaptured(s string) (string, error) {
	var buf bytes.Buffer
	sub := sh.fork()
	sub.Stdout = &buf
	// `exit` inside $(...) ends the substitution, not the parent shell.
	catchExit(func() int { return sub.EvalString(s) })
	return buf.String(), nil
}

// fork creates a child shell inheriting vars/exported/aliases/funcs.
func (sh *Shell) fork() *Shell {
	child := &Shell{
		name:      sh.name,
		vars:      make(map[string]string),
		arrays:    make(map[string][]string),
		assoc:     make(map[string]map[string]string),
		exported:  make(map[string]bool),
		aliases:   make(map[string]string),
		funcs:     make(map[string]*parser.FuncDef),
		posParams: sh.posParams,
		jobs:      sh.jobs,
		traps:     sh.traps,
		Stdin:     sh.Stdin,
		Stdout:    sh.Stdout,
		Stderr:    sh.Stderr,
		lastExit:  sh.lastExit,
	}
	for k, v := range sh.vars {
		child.vars[k] = v
	}
	for k, v := range sh.arrays {
		child.arrays[k] = append([]string(nil), v...)
	}
	for k, m := range sh.assoc {
		cm := make(map[string]string, len(m))
		for kk, vv := range m {
			cm[kk] = vv
		}
		child.assoc[k] = cm
	}
	for k, v := range sh.exported {
		child.exported[k] = v
	}
	for k, v := range sh.aliases {
		child.aliases[k] = v
	}
	for k, v := range sh.funcs {
		child.funcs[k] = v
	}
	return child
}

// heredocSpec records a pending heredoc found on a command line.
type heredocSpec struct {
	delim      string
	expand     bool // true = unquoted delimiter: body expands $VAR etc.
	strip      bool // true for <<-
	cmdLineIdx int  // index into outLines whose command line holds this heredoc
	insertPos  int  // rune offset just past the delimiter where the body marker goes
}

// preprocessHeredocs scans a multiline input string for heredoc operators,
// collects their body lines from subsequent lines, and re-encodes the bodies
// as inline sentinel markers (\x01...\x02 for expanding, \x03...\x02 for literal)
// that the lexer emits as HEREDOC_BODY tokens directly after the delimiter token.
func preprocessHeredocs(input string) string {
	if !strings.Contains(input, "<<") {
		return input
	}
	// Already preprocessed: command-substitution content carries heredoc bodies
	// that an outer pass inlined as \x01.. / \x03.. markers. Re-splitting on the
	// markers' embedded newlines would corrupt them, so leave such input alone.
	if strings.ContainsAny(input, "\x01\x03") {
		return input
	}
	lines := strings.Split(input, "\n")

	// One pending body marker to splice into a command line, at insertPos.
	type insertion struct {
		pos    int
		marker string
	}
	var outLines []string
	var pending []heredocSpec
	var pendingBodies [][]string
	insertions := map[int][]insertion{} // by command-line index

	for _, line := range lines {
		if len(pending) > 0 {
			s := pending[0]
			checkLine := line
			if s.strip {
				checkLine = strings.TrimLeft(line, "\t")
			}
			if checkLine == s.delim {
				// Terminator found: record the body marker to splice in just past
				// the delimiter (so a following "| cmd" or redirection is not
				// separated from its command).
				body := strings.Join(pendingBodies[0], "\n")
				if len(pendingBodies[0]) > 0 {
					body += "\n"
				}
				var marker byte = '\x01' // expanding
				if !s.expand {
					marker = '\x03' // literal
				}
				insertions[s.cmdLineIdx] = append(insertions[s.cmdLineIdx], insertion{
					pos:    s.insertPos,
					marker: " " + string(marker) + body + "\x02",
				})
				pending = pending[1:]
				pendingBodies = pendingBodies[1:]
			} else {
				bodyLine := line
				if s.strip {
					bodyLine = strings.TrimLeft(line, "\t")
				}
				pendingBodies[0] = append(pendingBodies[0], bodyLine)
			}
		} else {
			cmdLineIdx := len(outLines)
			outLines = append(outLines, line)
			specs := scanLineForHeredocs(line, cmdLineIdx)
			for _, s := range specs {
				pending = append(pending, s)
				pendingBodies = append(pendingBodies, nil)
			}
		}
	}

	// Splice the collected markers into their command lines. Insertions for a
	// line were recorded left-to-right (ascending pos), so apply them in reverse
	// (rightmost first) to keep the earlier offsets valid.
	for idx, ins := range insertions {
		runes := []rune(outLines[idx])
		for k := len(ins) - 1; k >= 0; k-- {
			p := ins[k].pos
			if p > len(runes) {
				p = len(runes)
			}
			runes = append(runes[:p:p], append([]rune(ins[k].marker), runes[p:]...)...)
		}
		outLines[idx] = string(runes)
	}

	return strings.Join(outLines, "\n")
}

// scanLineForHeredocs finds all heredoc operators on a single command line.
// It skips <<< (here-strings) and handles single/double-quoted contexts.
func scanLineForHeredocs(line string, cmdLineIdx int) []heredocSpec {
	var specs []heredocSpec
	runes := []rune(line)
	inSQ, inDQ := false, false

	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if inSQ {
			if ch == '\'' {
				inSQ = false
			}
			continue
		}
		if inDQ {
			if ch == '"' {
				inDQ = false
			}
			continue
		}
		if ch == '#' {
			break
		}
		if ch == '\'' {
			inSQ = true
			continue
		}
		if ch == '"' {
			inDQ = true
			continue
		}
		if ch == '<' && i+1 < len(runes) && runes[i+1] == '<' {
			j := i + 2
			if j < len(runes) && runes[j] == '<' {
				// <<< here-string — skip
				i = j
				continue
			}
			strip := false
			if j < len(runes) && runes[j] == '-' {
				strip = true
				j++
			}
			for j < len(runes) && (runes[j] == ' ' || runes[j] == '\t') {
				j++
			}
			if j >= len(runes) {
				continue
			}
			var delim string
			expand := true
			qch := runes[j]
			if qch == '\'' || qch == '"' {
				expand = false
				j++
				start := j
				for j < len(runes) && runes[j] != qch {
					j++
				}
				delim = string(runes[start:j])
				if j < len(runes) {
					j++
				}
			} else {
				start := j
				for j < len(runes) && runes[j] != ' ' && runes[j] != '\t' {
					j++
				}
				delim = string(runes[start:j])
			}
			if delim != "" {
				specs = append(specs, heredocSpec{
					delim:      delim,
					expand:     expand,
					strip:      strip,
					cmdLineIdx: cmdLineIdx,
					insertPos:  j, // just past the delimiter (before any "| cmd", redirs, etc.)
				})
			}
			i = j - 1
		}
	}
	return specs
}
