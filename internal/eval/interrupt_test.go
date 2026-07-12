package eval

import (
	"bytes"
	"sync/atomic"
	"testing"
	"time"
)

// TestInterruptStopsForegroundLoop reproduces the reported bug: Ctrl+C could
// not cancel a running loop like `while true; do ...; done` -- only the one
// external command that happened to be running (e.g. sleep) got interrupted,
// and the loop itself just continued to its next iteration. Setting the
// interrupt flag the same way WatchInterrupts' signal handler would must stop
// even a loop with no external commands at all, reporting exit code 130
// (128+SIGINT, bash's convention).
//
// This does not exercise real OS signal delivery (Ctrl+C in an actual
// terminal) -- os.Process.Signal(syscall.SIGINT) isn't supported on Windows,
// and there's no portable way to self-deliver a console CTRL_C_EVENT from a
// test. WatchInterrupts wires the flag to signal.Notify(ch, os.Interrupt),
// the identical mechanism evalSimpleCmd's existing (and already working)
// per-command interrupt forwarding relies on.
func TestInterruptStopsForegroundLoop(t *testing.T) {
	// checkInterrupt's CompareAndSwap already clears the flag on the success
	// path; this is only a safety net for the failure path (loop didn't stop),
	// so a bug here can't also contaminate every later test in the binary --
	// interrupted is process-global, shared by the whole test binary.
	defer atomic.StoreInt32(&interrupted, 0)

	sh := New("posh")
	var buf bytes.Buffer
	sh.Stdout, sh.Stderr = &buf, &buf

	done := make(chan int, 1)
	go func() { done <- sh.EvalString("while true; do :; done") }()

	time.Sleep(200 * time.Millisecond)
	atomic.StoreInt32(&interrupted, 1)

	select {
	case code := <-done:
		if code != 130 {
			t.Fatalf("code = %d, want 130", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not stop within 5s of the interrupt flag")
	}
}

// TestInterruptDoesNotAffectBackgroundJob mirrors bash's job control: a
// foreground Ctrl+C must not stop a job already running in the background.
func TestInterruptDoesNotAffectBackgroundJob(t *testing.T) {
	// checkInterrupt is a no-op for a background shell, so nothing ever
	// consumes/clears the flag this test sets below on its own. interrupted is
	// process-global (shared by every test in this package's test binary), so
	// leaving it at 1 would spuriously abort loops/lists in whichever tests
	// happen to run afterward. Always restore it when this test exits.
	defer atomic.StoreInt32(&interrupted, 0)

	sh := New("posh")
	var buf bytes.Buffer
	sh.Stdout, sh.Stderr = &buf, &buf

	done := make(chan int, 1)
	go func() {
		done <- sh.EvalString(`i=0; while [ $i -lt 100000 ]; do i=$(( i + 1 )); done &`)
	}()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("backgrounding statement should return 0 immediately, got %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backgrounding statement should return immediately")
	}

	atomic.StoreInt32(&interrupted, 1)
	time.Sleep(300 * time.Millisecond)
	// The foreground shell (not the backgrounded one) must remain usable.
	if code := sh.EvalString("echo ok"); code != 0 {
		t.Fatalf("shell should remain usable after an interrupt, got code %d", code)
	}
}

// TestExitStillPropagatesPastEvalStringAt ensures the exit builtin still ends
// the whole session: EvalStringAt catches shellInterrupt but must not also
// absorb shellExit, which needs to keep propagating to the top-level recover
// in main (see ExitCode).
func TestExitStillPropagatesPastEvalStringAt(t *testing.T) {
	sh := New("posh")
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected exit to panic through EvalString, got no panic")
		}
		if code, ok := ExitCode(r); !ok || code != 3 {
			t.Fatalf("ExitCode = %d, %v; want 3, true", code, ok)
		}
	}()
	sh.EvalString("exit 3")
	t.Fatal("unreachable: exit should have panicked")
}
