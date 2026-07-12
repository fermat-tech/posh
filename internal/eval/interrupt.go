package eval

import (
	"os"
	"os/signal"
	"sync/atomic"
)

// interrupted is set the instant Ctrl+C (SIGINT) is received and cleared the
// next time a foreground command checks it (see checkInterrupt). This is what
// lets a loop or statement list abort the command it's currently running back
// to the prompt, matching bash's SIGINT handling -- even when no external
// process is involved at all, e.g. a tight `while true; do :; done`.
//
// This is deliberately separate from the per-external-command interrupt
// forwarding in evalSimpleCmd (which kills only the one running child process
// immediately): that still fires independently and is what makes something
// like `sleep 5` stop right away, while this flag is what then lets the
// enclosing loop notice the interrupt too and stop re-entering, rather than
// silently starting its next iteration.
var interrupted int32

// WatchInterrupts installs a persistent SIGINT handler for the process. It
// must be called once at startup, before the REPL or any script runs.
// Besides setting the flag checkInterrupt polls, registering this handler is
// also what keeps posh itself alive across Ctrl+C: on Windows, an unhandled
// CTRL_C_EVENT can terminate the process outright, so a handler must always be
// "listening" even between commands, at the prompt, or when a background job
// also receives the console's event.
func WatchInterrupts() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	go func() {
		for range ch {
			atomic.StoreInt32(&interrupted, 1)
		}
	}()
}

// shellInterrupt unwinds the currently executing foreground command back to
// its nearest boundary when Ctrl+C is pressed, the same way shellExit unwinds
// for the exit builtin. catchExit reports it as exit code 130 (128+SIGINT),
// matching bash's convention; EvalStringAt (the outermost per-command
// boundary used by the REPL and script execution) does the same but is not
// implemented via catchExit, since catchExit also absorbs shellExit and a
// top-level `exit` must keep propagating past that point to end the session.
type shellInterrupt struct{}

// checkInterrupt panics with shellInterrupt if Ctrl+C has been received since
// the flag was last cleared. A background job (sh.isBackground) never observes
// the foreground's Ctrl+C: bash's job control keeps that from reaching
// background jobs via process groups, and posh's background shells get the
// same protection by simply not checking the global flag here (background
// external processes are additionally protected at the OS level via their own
// process group; see setBackgroundAttrs). A background job still checks its
// own jobKill flag, though — how `kill %n` stops a backgrounded compound
// command (a subshell, loop, or group running as a goroutine with no OS
// process of its own to send a real signal to); see jobs.go.
func (sh *Shell) checkInterrupt() {
	if sh.isBackground {
		if sh.jobKill != nil && atomic.CompareAndSwapInt32(sh.jobKill, 1, 0) {
			panic(shellInterrupt{})
		}
		return
	}
	if atomic.CompareAndSwapInt32(&interrupted, 1, 0) {
		panic(shellInterrupt{})
	}
}
