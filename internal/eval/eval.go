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

// Shell holds the execution state for a posh session.
type Shell struct {
	name      string            // argv[0] / $0
	vars      map[string]string // shell variables
	exported  map[string]bool   // names exported to child processes
	aliases   map[string]string
	funcs     map[string]*parser.FuncDef
	posParams []string // $1 $2 ... (set for scripts/functions)
	lastExit  int      // $?
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
	return sh
}

// GetVar is the exported accessor for use outside the package.
func (sh *Shell) GetVar(name string) string { return sh.getVar(name) }
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
	return ""
}

func (sh *Shell) setVar(name, val string) {
	sh.vars[name] = val
}

// SetPosParams sets $1, $2, ... from the given slice.
func (sh *Shell) SetPosParams(params []string) {
	sh.posParams = params
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
	}
	return 0
}

// EvalString parses and evaluates a string.
func (sh *Shell) EvalString(s string) int {
	s = preprocessHeredocs(s)
	node, err := parser.Parse(s)
	if err != nil {
		fmt.Fprintf(sh.Stderr, "%s: %v\n", sh.name, err)
		sh.lastExit = 1
		return 1
	}
	code := sh.Eval(node)
	sh.lastExit = code
	return code
}

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
		go child.Eval(n)
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
			code := sub.evalNodeIO(node, in, out, sh.Stderr)
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
		rIn, rOut, rErr, cleanup, err := applyRedirs(sub.Redirs, sh.Stdin, sh.Stdout, sh.Stderr)
		if err != nil {
			fmt.Fprintf(sh.Stderr, "%s: %v\n", sh.name, err)
			return 1
		}
		defer cleanup()
		child.Stdin, child.Stdout, child.Stderr = rIn, rOut, rErr
		code = child.evalList(sub.Body)
	}
	return code
}

func (sh *Shell) evalGroupCmd(grp *parser.GroupCmd) int {
	rIn, rOut, rErr, cleanup, err := applyRedirs(grp.Redirs, sh.Stdin, sh.Stdout, sh.Stderr)
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
	rIn, rOut, rErr, cleanup, err := applyRedirs(cmd.Redirs, sh.Stdin, sh.Stdout, sh.Stderr)
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
	rIn, rOut, rErr, cleanup, err := applyRedirs(cmd.Redirs, sh.Stdin, sh.Stdout, sh.Stderr)
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
	rIn, rOut, rErr, cleanup, err := applyRedirs(cmd.Redirs, sh.Stdin, sh.Stdout, sh.Stderr)
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
	rIn, rOut, rErr, cleanup, err := applyRedirs(cmd.Redirs, sh.Stdin, sh.Stdout, sh.Stderr)
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

func (sh *Shell) callFunc(def *parser.FuncDef, args []string) (code int) {
	child := sh.fork()
	child.posParams = args

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
			idx := strings.IndexByte(a, '=')
			sh.setVar(a[:idx], sh.expandWord(a[idx+1:]))
		}
		return 0
	}

	// Apply redirections
	rStdin, rStdout, rStderr, cleanup, err := applyRedirs(cmd.Redirs, stdin, stdout, stderr)
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

	// Build inline environment
	cmdEnv := sh.exportedEnv()
	for _, a := range cmd.Assigns {
		idx := strings.IndexByte(a, '=')
		key := a[:idx]
		val := sh.expandWord(a[idx+1:])
		cmdEnv = append(cmdEnv, key+"="+val)
	}

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
		code := sh.callFunc(fn, words[1:])
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
func (sh *Shell) runCaptured(s string) (string, error) {
	var buf bytes.Buffer
	sub := sh.fork()
	sub.Stdout = &buf
	sub.EvalString(s)
	return buf.String(), nil
}

// fork creates a child shell inheriting vars/exported/aliases/funcs.
func (sh *Shell) fork() *Shell {
	child := &Shell{
		name:      sh.name,
		vars:      make(map[string]string),
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

// preprocessHeredocs inlines heredoc content from multi-line input strings.
// It replaces <<DELIM / <<-DELIM markers with HEREDOC_CONTENT tokens embedded
// as a special encoding the evaluator's applyRedirs knows how to handle.
// This is a line-level preprocessor that runs before tokenization.
func preprocessHeredocs(input string) string {
	// Simple implementation: scan for <<DELIM patterns and gather content.
	// We store collected heredoc content in the Redir.File field by
	// re-encoding it as a special marker that the lexer will tokenize.
	// For now, heredoc content is passed through the string unchanged;
	// applyRedirs handles Redir.File for HEREDOC_OP ops.
	return input
}
