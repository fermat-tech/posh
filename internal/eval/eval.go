// Package eval executes a posh AST.
package eval

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/fermat-tech/posh/internal/parser"
)

// Shell holds the execution state for a posh session.
type Shell struct {
	name     string            // argv[0] / $0
	vars     map[string]string // shell variables
	exported map[string]bool   // names exported to child processes
	aliases  map[string]string
	lastExit int // $?
	jobs     *JobTable

	// I/O streams for this shell instance (may be redirected in subshells)
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// history is managed by the REPL; eval just needs the slice
	History []string
}

// New creates a Shell with the given name and inherits the OS environment.
func New(name string) *Shell {
	sh := &Shell{
		name:     name,
		vars:     make(map[string]string),
		exported: make(map[string]bool),
		aliases:  make(map[string]string),
		jobs:     newJobTable(),
		Stdin:    os.Stdin,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
	}
	// Seed vars from environment
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

// GetVar is the exported form of getVar for use outside the package.
func (sh *Shell) GetVar(name string) string { return sh.getVar(name) }

// getVar looks up a shell variable.
func (sh *Shell) getVar(name string) string {
	if v, ok := sh.vars[name]; ok {
		return v
	}
	return ""
}

// setVar sets a shell variable.
func (sh *Shell) setVar(name, val string) {
	sh.vars[name] = val
}

// exportedEnv returns all exported variables as a KEY=VAL slice.
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
	case *parser.SimpleCmd:
		return sh.evalSimpleCmd(n, sh.Stdin, sh.Stdout, sh.Stderr, false)
	}
	return 0
}

// EvalString parses and evaluates a string.
func (sh *Shell) EvalString(s string) int {
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
	code := sh.evalNode(list.First, false)
	for _, elem := range list.Elems {
		if elem.Node == nil {
			// Trailing & — already handled
			continue
		}
		switch elem.Op {
		case parser.OpSemi:
			code = sh.evalNode(elem.Node, false)
		case parser.OpAnd:
			if code == 0 {
				code = sh.evalNode(elem.Node, false)
			}
		case parser.OpOr:
			if code != 0 {
				code = sh.evalNode(elem.Node, false)
			}
		case parser.OpAmp:
			sh.evalNode(elem.Node, true) // background
			code = 0
		}
	}
	return code
}

func (sh *Shell) evalNode(n parser.Node, background bool) int {
	if n == nil {
		return 0
	}
	switch v := n.(type) {
	case *parser.Pipeline:
		if background {
			go sh.evalPipeline(v)
			return 0
		}
		return sh.evalPipeline(v)
	case *parser.Subshell:
		if background {
			go sh.evalSubshell(v)
			return 0
		}
		return sh.evalSubshell(v)
	case *parser.List:
		return sh.evalList(v)
	}
	return 0
}

func (sh *Shell) evalPipeline(pipe *parser.Pipeline) int {
	if len(pipe.Cmds) == 1 {
		code := sh.evalSimpleCmd(pipe.Cmds[0], sh.Stdin, sh.Stdout, sh.Stderr, false)
		if pipe.Negate {
			if code == 0 {
				return 1
			}
			return 0
		}
		return code
	}

	// Multi-command pipeline: wire stdout of cmd[i] → stdin of cmd[i+1]
	cmds := pipe.Cmds
	n := len(cmds)
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

	for i, cmd := range cmds {
		var stdin io.Reader = sh.Stdin
		var stdout io.Writer = sh.Stdout
		if i > 0 {
			stdin = pipes[i-1]
		}
		if i < n-1 {
			stdout = pipeW[i]
		}
		go func(idx int, c *parser.SimpleCmd, in io.Reader, out io.Writer) {
			code := sh.evalSimpleCmd(c, in, out, sh.Stderr, false)
			if idx < n-1 {
				pipeW[idx].Close()
			}
			results[idx] <- result{code}
		}(i, cmd, stdin, stdout)
	}

	// Collect results; last command's exit code is the pipeline's exit code
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
	return lastCode
}

func (sh *Shell) evalSubshell(sub *parser.Subshell) int {
	child := sh.fork()
	if sub.Body == nil {
		return 0
	}
	return child.evalList(sub.Body)
}

// fork creates a child shell that inherits vars/exported/aliases but has its own maps.
func (sh *Shell) fork() *Shell {
	child := &Shell{
		name:     sh.name,
		vars:     make(map[string]string),
		exported: make(map[string]bool),
		aliases:  make(map[string]string),
		jobs:     sh.jobs, // shared job table
		Stdin:    sh.Stdin,
		Stdout:   sh.Stdout,
		Stderr:   sh.Stderr,
		lastExit: sh.lastExit,
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
	return child
}

// evalSimpleCmd executes a single command.
func (sh *Shell) evalSimpleCmd(cmd *parser.SimpleCmd, stdin io.Reader, stdout, stderr io.Writer, background bool) int {
	// Apply inline assignments (VAR=val with no command name — export to shell)
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

	// Build inline environment for this command
	cmdEnv := sh.exportedEnv()
	for _, a := range cmd.Assigns {
		idx := strings.IndexByte(a, '=')
		key := a[:idx]
		val := sh.expandWord(a[idx+1:])
		cmdEnv = append(cmdEnv, key+"="+val)
	}

	// Alias expansion (first word only)
	name := words[0]
	if expanded, ok := sh.aliases[name]; ok {
		// Re-parse the alias expansion prepended to the rest of the arguments
		full := expanded + " " + strings.Join(words[1:], " ")
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

	if background {
		if err := c.Start(); err != nil {
			fmt.Fprintf(rStderr, "%s: %v\n", sh.name, err)
			return 1
		}
		sh.jobs.add(c, strings.Join(words, " "))
		return 0
	}

	if err := c.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			sh.lastExit = code
			return code
		}
		fmt.Fprintf(rStderr, "%s: %v\n", sh.name, err)
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
