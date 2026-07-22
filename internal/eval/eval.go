// Package eval executes a posh AST.
package eval

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
// context rather than the whole process. A shellInterrupt (Ctrl+C; see
// interrupt.go) is absorbed the same way, reported as exit code 130
// (128+SIGINT, matching bash) -- this boundary is also what stops a
// shellInterrupt from escaping a subshell/pipeline-stage/background-job
// goroutine uncaught, which would otherwise crash the process. Other panics
// propagate unchanged.
func catchExit(fn func() int) (code int) {
	defer func() {
		if r := recover(); r != nil {
			if ex, ok := r.(shellExit); ok {
				code = ex.code
				return
			}
			if _, ok := r.(shellInterrupt); ok {
				code = 130
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
	isBackground bool              // true anywhere within a background job's execution tree; propagates through fork()
	jobKill      *int32            // this job's kill flag, if isBackground (see checkInterrupt); propagates through fork()
	jobProc      *jobProcSet       // this job's currently-running-process set, if isBackground (see jobs.go); propagates through fork()

	// backgroundLaunch is true only for the immediate child shell evalNode
	// creates for a node marked background=true (a statement with a trailing
	// &) -- never inherited by any further fork() a nested execution makes.
	// evalSimpleCmd reads this (not isBackground) to decide whether THIS one
	// external command should detach as its own new OS process and register
	// as a new Job. Without this distinction, an external command anywhere
	// inside an already-backgrounded compound command (e.g. `sleep 1` inside
	// `(while true; do echo hi; sleep 1; done) &`, which has no & of its own)
	// would also detach and self-register as a new job every time it ran --
	// non-blocking, so the loop span at full speed with no real delay, firing
	// off one leaked job per iteration.
	backgroundLaunch bool

	// bgReady is set alongside backgroundLaunch (never propagated by fork()).
	// evalSimpleCmd closes it at each point where it has decided whether this
	// backgrounded plain command will register a process-backed Job -- a
	// builtin/function call (never registers one), command-not-found, or a
	// successful/failed process Start()+jobs.add(). evalNode's background
	// dispatch waits on it before returning, so a job that WILL be
	// registered is guaranteed to be in the table by the time control
	// reaches the next statement. Without this, registration happened
	// entirely inside the spawned goroutine with no synchronization at all:
	// a script with no delay between `cmd &` and the next line (e.g.
	// `sleep 3 &; jobs -l`) could see an empty job list, since the goroutine
	// might not have been scheduled yet. Builtins/functions still run
	// asynchronously -- signaling ready happens right before invoking them,
	// not after they finish.
	bgReady chan struct{}

	// I/O streams for this shell instance
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// Raw console handles for the interactive terminal, if any. main sets these
	// to os.Stdout/os.Stderr while Stdout/Stderr are colorable wrappers. When a
	// foreground external command's output is the terminal (unredirected,
	// unpiped), it is given these raw *os.File handles instead of the wrapper so
	// the child sees a real console: tools like git then detect a TTY and enable
	// color and paging. nil in non-interactive contexts (scripts, tests).
	//
	// TermOut/TermErr hold the matching colorable wrapper writers (the terminal
	// sink itself). A child inherits the raw console only when its output writer
	// is exactly that sink — i.e. genuinely the terminal. Comparing against the
	// live Stdout would misfire inside a pipeline, where a stage's Stdout is the
	// pipe writer, and wrongly send the stage's output to the console instead.
	ConsoleOut *os.File
	ConsoleErr *os.File
	TermOut    io.Writer
	TermErr    io.Writer

	// startTime is when this session began, for $SECONDS. Propagated through
	// fork() so a subshell's $SECONDS keeps counting from the top-level
	// session's start, matching bash rather than resetting to zero.
	startTime time.Time
	// secondsOffset lets SECONDS=n (see setVar) reset the elapsed counter
	// without needing a real, mutable "now" reference; also propagated
	// through fork().
	secondsOffset int64

	History []string
}

// cachedHostname and cachedExecutable memoize os.Hostname/os.Executable: both
// are process-wide facts that never change between calls, but New() runs once
// per Shell (once per subshell, once per test, ...), and the underlying
// syscalls are not free -- calling them unconditionally on every Shell made
// the whole test suite measurably slower, which was in turn shifting the
// timing window on an already-flaky, unrelated external-process test. Compute
// each at most once per process instead.
var (
	hostnameOnce sync.Once
	hostnameVal  string
	execOnce     sync.Once
	execVal      string
)

func cachedHostname() string {
	hostnameOnce.Do(func() {
		hostnameVal, _ = os.Hostname()
	})
	return hostnameVal
}

func cachedExecutable() string {
	execOnce.Do(func() {
		execVal, _ = os.Executable()
	})
	return execVal
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
	// IFS is normally already set by whatever started this process (matching a
	// login shell), but give it bash's default explicitly if not, so it's
	// visible to `env`/`set` rather than only implicitly assumed by wordSplit.
	if sh.vars["IFS"] == "" {
		sh.vars["IFS"] = " \t\n"
	}
	// PWD, unlike most of the below, is also kept live by builtinCd on every
	// `cd` -- this just seeds it at startup, matching bash.
	if wd, err := os.Getwd(); err == nil {
		sh.vars["PWD"] = wd
	}
	// USER/HOSTNAME/SHELL aren't standard Windows env vars (Windows sets
	// USERNAME/COMPUTERNAME/ComSpec instead), so synthesize posh's own bash-name
	// equivalents when the environment didn't already provide them.
	if sh.vars["USER"] == "" {
		if u := sh.vars["USERNAME"]; u != "" {
			sh.vars["USER"] = u
			sh.exported["USER"] = true
		}
	}
	if sh.vars["HOSTNAME"] == "" {
		if h := cachedHostname(); h != "" {
			sh.vars["HOSTNAME"] = h
			sh.exported["HOSTNAME"] = true
		}
	}
	if sh.vars["SHELL"] == "" {
		if exe := cachedExecutable(); exe != "" {
			sh.vars["SHELL"] = exe
			sh.exported["SHELL"] = true
		}
	}
	sh.startTime = time.Now()
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

// ArrayNames returns the names of all indexed and associative array variables
// (e.g. POSH_VERSINFO), for tab completion. Vars() alone misses these since
// they live in separate maps, not sh.vars.
func (sh *Shell) ArrayNames() []string {
	names := make([]string, 0, len(sh.arrays)+len(sh.assoc))
	for k := range sh.arrays {
		names = append(names, k)
	}
	for k := range sh.assoc {
		names = append(names, k)
	}
	return names
}
// signalBgLaunchReady closes this shell's bgReady channel exactly once, if it
// has one (see the field doc). Safe to call from multiple decision points in
// evalSimpleCmd since it nils the field after closing.
func (sh *Shell) signalBgLaunchReady() {
	if sh.bgReady != nil {
		close(sh.bgReady)
		sh.bgReady = nil
	}
}

func (sh *Shell) GetOpt(name string) bool   { return sh.opts[name] }
func (sh *Shell) SetOpt(name string, val bool) {
	if val {
		sh.opts[name] = true
	} else {
		delete(sh.opts, name)
	}
}

// optFlags returns $-'s value: the bash single-letter mnemonic for each
// currently-enabled `set -o` option that has one. vi/emacs/pipefail have no
// single-letter form in bash either, so they're never reflected here even
// when set. Bash's $- also includes flags for session-level state posh has
// no equivalent of (interactive, job control, restricted, ...); those are
// simply omitted rather than guessed at.
func (sh *Shell) optFlags() string {
	var sb strings.Builder
	// Bash's own canonical order for the flags that have both a short letter
	// and a `set -o` long name.
	for _, o := range []struct{ name string; letter byte }{
		{"errexit", 'e'},
		{"noglob", 'f'},
		{"nounset", 'u'},
		{"verbose", 'v'},
		{"xtrace", 'x'},
	} {
		if sh.opts[o.name] {
			sb.WriteByte(o.letter)
		}
	}
	return sb.String()
}

// DynamicVarNames lists the built-in variables getVar computes on the fly
// (see there) rather than storing in sh.vars. Because they're never actually
// present in that map, Vars() alone can never offer them -- callers that
// enumerate variable names for a purpose other than a literal $VAR expansion
// (e.g. tab completion) need to union this list in too, or these silently
// never show up. Kept as the single source of truth so the two can't drift.
var DynamicVarNames = []string{"RANDOM", "SECONDS", "POSHPID"}

func (sh *Shell) getVar(name string) string {
	// Dynamic variables: computed fresh on every reference rather than stored,
	// so a plain sh.vars lookup would never see the right value. Checked before
	// sh.vars so an assignment to one of these (e.g. RANDOM=5, mimicking bash's
	// reseed) doesn't "freeze" it -- SECONDS is the one exception, handled via
	// secondsOffset in setVar, since resetting the elapsed counter (SECONDS=0)
	// is a common, useful idiom that RANDOM's reseed isn't worth matching here.
	switch name {
	case "RANDOM":
		return strconv.Itoa(rand.Intn(32768))
	case "SECONDS":
		elapsed := int64(time.Since(sh.startTime).Seconds()) + sh.secondsOffset
		return strconv.FormatInt(elapsed, 10)
	case "POSHPID":
		// Unlike bash's $BASHPID, this always equals $$ -- posh's ( ... )
		// subshells run as an in-process goroutine, not a real forked OS
		// process, so there is no separate PID for them to differ by.
		return strconv.Itoa(os.Getpid())
	}
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
	if name == "SECONDS" {
		// SECONDS=n resets the elapsed counter to n, matching bash: future
		// reads keep ticking up from there rather than jumping back to the
		// real elapsed time. Silently ignored (as if unset) for a non-integer
		// value, same as bash.
		if n, err := strconv.ParseInt(val, 10, 64); err == nil {
			sh.secondsOffset = n - int64(time.Since(sh.startTime).Seconds())
		}
		return
	}
	sh.vars[name] = val
}

// SetPosParams sets $1, $2, ... from the given slice.
func (sh *Shell) SetPosParams(params []string) {
	sh.posParams = params
}

// SetName sets the shell's name, which is what $0 expands to and the prefix used
// on error messages. For an interactive shell this is the interpreter name; when
// executing a script it should be the script's path, matching bash.
func (sh *Shell) SetName(name string) {
	sh.name = name
}

// SetVersion sets $POSH_VERSION and the $POSH_VERSINFO array, mirroring bash's
// $BASH_VERSION / $BASH_VERSINFO. version is the release tag as reported by
// `posh --version` (e.g. "v1.3.51", or "dev" for a build with no tag supplied).
func (sh *Shell) SetVersion(version string) {
	sh.vars["POSH_VERSION"] = version
	major, minor, patch := parseVersionParts(version)
	// Bash's array is (major minor patchlevel build release machtype); posh has
	// no separate build/release concept beyond the tag itself, so the array is
	// (major minor patch machtype). A bare $POSH_VERSINFO (no index) yields
	// element 0, the major version — matching $BASH_VERSINFO's behavior.
	sh.arrays["POSH_VERSINFO"] = []string{major, minor, patch, runtime.GOOS + "-" + runtime.GOARCH}
}

// parseVersionParts splits a "vMAJOR.MINOR.PATCH"-shaped version string into its
// three numeric components, defaulting any missing or non-numeric part to "0"
// (e.g. for an untagged "dev" build) rather than failing.
func parseVersionParts(v string) (major, minor, patch string) {
	parts := strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3)
	get := func(i int) string {
		if i >= len(parts) {
			return "0"
		}
		if _, err := strconv.Atoi(parts[i]); err != nil {
			return "0"
		}
		return parts[i]
	}
	return get(0), get(1), get(2)
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

	case *parser.CondCmd:
		return sh.evalCondCmd(n)
	}
	return 0
}

// EvalString parses and evaluates a string.
func (sh *Shell) EvalString(s string) int {
	return sh.EvalStringAt(s, 1)
}

// EvalStringAt parses and evaluates s, reporting errors with lineBase as the
// first line number.
//
// If Ctrl+C interrupts the running command (a shellInterrupt panic; see
// interrupt.go), evaluation stops here and 130 is returned (128+SIGINT,
// bash's convention) instead of letting the panic escape further -- this is
// the outermost per-command boundary, where a foreground Ctrl+C should stop
// just the command that was running, not the whole session. This is
// deliberately not implemented via catchExit: a shellExit (the exit builtin)
// must keep propagating past this point untouched, so a top-level `exit`
// still ends the session rather than being absorbed here alongside interrupts.
func (sh *Shell) EvalStringAt(s string, lineBase int) (code int) {
	// Clear any stale interrupt from before this command started. WatchInterrupts
	// is a persistent, process-wide handler: pressing Ctrl+C just to abort or
	// retype a botched command line at the prompt (handled separately by the
	// line editor) ALSO sets this flag, since it fires on every Ctrl+C whether
	// or not a command is running. Left uncleared, that stale flag would be
	// silently consumed by the next checkInterrupt() call in the NEXT command --
	// which could be an unrelated, later statement in a `;`-list (e.g. `cd
	// ..;p`), dropping it with no error at all. A real Ctrl+C during THIS
	// command's own execution still takes effect normally, since checkInterrupt
	// is polled throughout, not just here.
	if !sh.isBackground {
		atomic.StoreInt32(&interrupted, 0)
	}
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(shellInterrupt); ok {
				code = 130
				sh.lastExit = 130
				return
			}
			panic(r)
		}
	}()
	s = preprocessHeredocs(s)
	node, err := parser.ParseAt(s, lineBase)
	if err != nil {
		fmt.Fprintf(sh.Stderr, "%s: %v\n", sh.name, err)
		sh.lastExit = 1
		sh.parseErr = true
		return 1
	}
	sh.parseErr = false
	code = sh.Eval(node)
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
	// Every loop body, loop condition, and top-level statement sequence passes
	// through here, making this the one place that needs to poll for Ctrl+C to
	// stop a `while true; do ...; done` (or any other loop) rather than just the
	// single command that happened to be running when the interrupt arrived.
	sh.checkInterrupt()

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
		sh.checkInterrupt()
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

		// A backgrounded plain command is left to evalSimpleCmd's own
		// isBackground handling, which for an external command already starts
		// a real OS process and registers a proper process-backed Job (see
		// jobs.go) -- that path reports a real PID and can deliver a real
		// signal, which this generic path cannot. Every other backgrounded
		// node (a subshell, loop, group, a real multi-stage pipeline, if/case,
		// ...) spawns no OS process of its own, so without this it runs as a
		// bare untracked goroutine: invisible to `jobs`, and with no way to
		// stop it via `kill %n` short of exiting the whole shell. Register a
		// goroutine-backed Job for those so job control can at least see and
		// stop them, even though there's no real process behind them.
		//
		// The parser always wraps a plain command in a single-Cmd *Pipeline
		// (see parsePipeline), while a compound command is only wrapped in one
		// if an actual | follows it (see maybeWrapInPipeline) -- so unwrap a
		// single-Cmd Pipeline before checking, or this would never match a
		// plain backgrounded command at all.
		target := n
		if pipe, ok := n.(*parser.Pipeline); ok && len(pipe.Cmds) == 1 {
			target = pipe.Cmds[0]
		}
		if _, isSimple := target.(*parser.SimpleCmd); isSimple {
			// Only this immediate command actually had the trailing & -- it,
			// and nothing nested any deeper, is what should detach as its own
			// OS process (see the backgroundLaunch field doc).
			child.backgroundLaunch = true
			ready := make(chan struct{})
			child.bgReady = ready
			// child is a forked copy -- child.vars is its own map, never the
			// same one sh.vars is, so anything evalSimpleCmd sets on child
			// (e.g. $_) is invisible to sh once this goroutine returns. $!
			// must land on sh (the shell that keeps running the rest of the
			// list), so capture it here instead: jobs are shared through the
			// same *JobTable (see fork()), so after the wait below, whatever
			// was JUST added is readable from sh.jobs.list() directly.
			before := len(sh.jobs.list())
			// `exit` in a background job ends that job only.
			go catchExit(func() int { return child.Eval(n) })
			// Wait just long enough to know whether a job was registered (see
			// bgReady's field doc) -- evalSimpleCmd signals this well before
			// any actual blocking work (a builtin/function running, or a
			// backgrounded process's own lifetime), so this does not turn
			// backgrounding into something that blocks the caller. The
			// timeout is a safety net for the handful of evalSimpleCmd paths
			// that return early without signaling (a bare assignment with no
			// command word, a redirection error, or a word that expanded to
			// nothing) -- rare edge cases, not worth instrumenting every
			// return statement for.
			select {
			case <-ready:
			case <-time.After(2 * time.Second):
			}
			if jobs := sh.jobs.list(); len(jobs) > before {
				if last := jobs[len(jobs)-1]; last.IsProcess() {
					sh.vars["!"] = strconv.Itoa(last.Cmd.Process.Pid)
				}
			}
			return 0
		}

		job, finish := sh.jobs.addGoroutine(describeNode(n))
		child.jobKill = job.kill
		child.jobProc = job.procs
		// Unlike a plain backgrounded command, a backgrounded compound
		// command has no real OS process of its own, so there is no PID for
		// $! to hold -- clear it rather than leave a stale value from
		// whatever was last backgrounded before this.
		sh.vars["!"] = ""
		go func() {
			defer finish()
			catchExit(func() int { return child.Eval(n) })
		}()
		return 0
	}
	return sh.Eval(n)
}

// describeNode returns a short, human-readable label for a backgrounded
// compound command, shown by `jobs -l` and in the "[N] Done" message. It is
// not a reconstruction of the original source text (posh's AST does not keep
// one) -- just enough to tell job entries apart at a glance.
func describeNode(n parser.Node) string {
	switch n.(type) {
	case *parser.Subshell:
		return "(subshell)"
	case *parser.GroupCmd:
		return "{ group }"
	case *parser.WhileCmd:
		return "while loop"
	case *parser.ForCmd:
		return "for loop"
	case *parser.IfCmd:
		return "if command"
	case *parser.CaseCmd:
		return "case command"
	case *parser.Pipeline:
		return "pipeline"
	case *parser.List:
		return "command list"
	default:
		return "compound command"
	}
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
	// Use real OS pipes (not io.Pipe) to connect stages. When a stage runs an
	// external command, the child inherits the pipe's *os.File directly as a real
	// fd and reads it on demand. With an in-memory io.Pipe, Go's exec would
	// instead spawn a goroutine that eagerly copies the reader into the child,
	// draining lines that the child never consumes — which broke loops like
	// `ls | while read e; do some_external_cmd; done` (the loop ended after one
	// iteration because the inner command's copier had swallowed the rest).
	pipes := make([]*os.File, n-1)
	pipeW := make([]*os.File, n-1)
	for i := range pipes {
		r, w, err := os.Pipe()
		if err != nil {
			fmt.Fprintf(sh.Stderr, "%s: pipe: %v\n", sh.name, err)
			// Close any pipes already created before bailing out.
			for j := 0; j < i; j++ {
				pipes[j].Close()
				pipeW[j].Close()
			}
			return 1
		}
		pipes[i], pipeW[i] = r, w
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
	// $_ -- the last word of the command about to run, matching bash (it
	// becomes "the previous command's last argument" once THIS command
	// finishes and the next one looks it up).
	sh.vars["_"] = words[len(words)-1]

	// Build the inline environment from the exported variables plus the
	// command-prefix assignments.
	cmdEnv := sh.inlineEnv(sh.exportedEnv(), cmd.Assigns)

	name := words[0]

	// Alias expansion
	if expanded, ok := sh.aliases[name]; ok {
		// The alias body (expanded) is trusted text that must be lexed and
		// expanded normally. The trailing arguments in words[1:], however, have
		// already been through full word expansion (quote-stripping, splitting,
		// globbing); re-lexing them bare would split/glob them a second time and
		// corrupt embedded whitespace (e.g. an arg of " : " would collapse to
		// ":"). Single-quote each so the re-parse takes them verbatim.
		full := expanded
		for _, w := range words[1:] {
			full += " " + shellSingleQuote(w)
		}
		node, perr := parser.Parse(full)
		if perr == nil && node != nil {
			sub := sh.fork()
			sub.Stdin, sub.Stdout, sub.Stderr = rStdin, rStdout, rStderr
			// Neither backgroundLaunch nor bgReady propagates through fork()
			// (see their field docs), but an aliased command stands in for the
			// original one -- `runit &`, where runit is aliased to an external
			// command, needs the same synchronous-registration handling
			// `sleep 3 &` gets. Hand ownership to sub so it signals readiness
			// instead of sh, which won't reach its own decision points at all
			// now that this alias has taken over evaluation.
			sub.backgroundLaunch = sh.backgroundLaunch
			sub.bgReady = sh.bgReady
			sh.bgReady = nil
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
		// A backgrounded builtin never registers a job (see the bgReady field
		// doc), so signal readiness now rather than after it finishes -- it
		// keeps running asynchronously, just untracked, exactly as before.
		sh.signalBgLaunchReady()
		code := bi(sh, words[1:], rStdin, rStdout, rStderr)
		sh.lastExit = code
		return code
	}

	// Shell function check
	if fn, ok := sh.funcs[name]; ok {
		// A backgrounded function call never registers a job either; see above.
		sh.signalBgLaunchReady()
		code := sh.callFunc(fn, words[1:], cmd.Assigns...)
		sh.lastExit = code
		return code
	}

	// External command
	resolvedPath, found := lookupCommand(name)
	if !found {
		sh.signalBgLaunchReady()
		fmt.Fprintf(rStderr, "%s: %s: command not found\n", sh.name, name)
		sh.lastExit = 127
		return 127
	}

	c := exec.Command(resolvedPath, words[1:]...)
	c.Env = cmdEnv
	c.Stdin = rStdin
	c.Stdout = rStdout
	c.Stderr = rStderr

	// When a foreground command's output still goes to the shell's terminal sink
	// (no redirection or pipe rewrote it), hand the child the raw console
	// *os.File rather than the colorable wrapper. Passing a non-*os.File makes
	// Go's exec splice in an OS pipe, which hides the console from the child so
	// git (and other TTY-aware tools) drop color and paging. The swap applies
	// only when the child's output is the terminal sink itself (TermOut/TermErr);
	// a redirection or pipe makes rStdout/rStderr something else, so it is left
	// untouched and the data flows to the file or next stage as intended.
	if !sh.isBackground {
		if sh.ConsoleOut != nil && rStdout == sh.TermOut {
			c.Stdout = sh.ConsoleOut
		}
		if sh.ConsoleErr != nil && rStderr == sh.TermErr {
			c.Stderr = sh.ConsoleErr
		}
	}

	if sh.backgroundLaunch {
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
			sh.signalBgLaunchReady()
			fmt.Fprintf(rStderr, "%s: %v\n", sh.name, err)
			return 1
		}
		sh.jobs.add(c, strings.Join(words, " "))
		// Signal readiness only now that the job is actually in the table --
		// this is the whole point: evalNode's background dispatch waits on
		// this so the very next statement is guaranteed to see the job via
		// `jobs -l`, `wait`, or `kill %n`, instead of racing the goroutine
		// that got us here.
		sh.signalBgLaunchReady()
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

	// If this command is running inside a background job's execution tree,
	// track it so RequestStop (`kill %n`) can kill it immediately -- without
	// this, killing the job only sets a flag checked between statements (see
	// checkInterrupt), so a job currently blocked here (e.g. `sleep 5` inside
	// a backgrounded loop) would keep running until this command finished
	// naturally on its own.
	if sh.jobProc != nil {
		sh.jobProc.add(c)
		defer sh.jobProc.remove(c)
	}

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

// shellSingleQuote wraps s in single quotes so it survives a re-lex verbatim,
// escaping any embedded single quotes the POSIX way ('\''). Used when rebuilding
// a command line from already-expanded words (e.g. alias expansion) so the
// second parse does not split, glob, or otherwise re-interpret them.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
	sub.name = path // $0 is the script being run, not the interpreter
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
		Stdin:      sh.Stdin,
		Stdout:     sh.Stdout,
		Stderr:     sh.Stderr,
		ConsoleOut:   sh.ConsoleOut,
		ConsoleErr:   sh.ConsoleErr,
		TermOut:      sh.TermOut,
		TermErr:      sh.TermErr,
		lastExit:     sh.lastExit,
		isBackground: sh.isBackground,
		jobKill:      sh.jobKill,
		jobProc:      sh.jobProc,
		startTime:      sh.startTime,
		secondsOffset:  sh.secondsOffset,
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
