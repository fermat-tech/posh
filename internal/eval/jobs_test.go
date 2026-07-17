package eval

import (
	"os"
	"testing"
	"time"
)

// TestBackgroundedPlainCommandRegistersJobSynchronously reproduces the race
// reported after the two fixes above shipped: registering a process-backed Job
// happened entirely inside the goroutine evalNode's background dispatch spawns
// and returns 0 without waiting for at all, so a script with no delay between
// `cmd &` and the very next statement (e.g. `sleep 3 &` then `jobs -l` on the
// next line) could see an empty job list -- the goroutine simply hadn't been
// scheduled yet. Unlike
// TestBackgroundedPlainCommandStillUsesRealProcess below (which tolerates a
// short poll, since it predates this fix), this asserts the job is visible the
// INSTANT EvalString returns, with no retry loop at all -- proving the race is
// actually closed, not just usually fast enough not to notice.
func TestBackgroundedPlainCommandRegistersJobSynchronously(t *testing.T) {
	sh := New("posh")
	buf := &syncBuf{}
	sh.Stdout, sh.Stderr = buf, buf

	if code := sh.EvalString("sleep 3 &"); code != 0 {
		t.Fatalf("backgrounding statement returned %d, want 0", code)
	}

	jobs := sh.jobs.list()
	if len(jobs) != 1 {
		t.Fatalf("jobs.list() immediately after EvalString = %d entries, want 1 (race not fixed)", len(jobs))
	}
	if !jobs[0].IsProcess() {
		t.Fatal("expected a real process-backed job")
	}
	jobs[0].RequestStop(os.Kill) // clean up rather than waiting out the full sleep
}

// TestBackgroundedSubshellRegistersJob reproduces the reported bug: `(while
// true; do ...; done) &` backgrounds a compound command (a subshell), which
// spawns no OS process of its own -- evalNode's generic background dispatch
// used to just fire an untracked goroutine, invisible to `jobs` and with no
// way to stop it via `kill %n` short of exiting the whole shell. It must now
// register a goroutine-backed Job (see jobs.go) that RequestStop can actually
// cancel.
func TestBackgroundedSubshellRegistersJob(t *testing.T) {
	sh := New("posh")
	buf := &syncBuf{}
	sh.Stdout, sh.Stderr = buf, buf

	done := make(chan int, 1)
	go func() { done <- sh.EvalString("(while true; do :; done) &") }()

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("backgrounding statement should return 0 immediately, got %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backgrounding statement should return immediately")
	}

	// Give the goroutine a moment to register (see the known, pre-existing
	// race documented on evalNode's background dispatch: registration is
	// asynchronous relative to the statement that launched it).
	var jobs []*Job
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		jobs = sh.jobs.list()
		if len(jobs) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs.list() = %d entries, want 1", len(jobs))
	}
	job := jobs[0]
	if job.IsProcess() {
		t.Fatal("a backgrounded subshell should be a goroutine job, not a process job")
	}

	// kill %1 (RequestStop) must actually stop the loop, not just be ignored.
	if err := job.RequestStop(os.Kill); err != nil {
		t.Fatalf("RequestStop: %v", err)
	}
	select {
	case <-job.done:
		// stopped successfully
	case <-time.After(2 * time.Second):
		t.Fatal("job did not stop within 2s of RequestStop -- kill %n has no effect")
	}
}

// TestKillStopsJobImmediatelyEvenMidSleep reproduces the reported bug: kill
// %n on a backgrounded compound command waited for the currently-running
// background command to finish before the job actually stopped, because the
// kill flag alone is only noticed between statements (see checkInterrupt) --
// not while blocked inside a long-running external command. RequestStop must
// also kill whatever process is currently running inside the job's execution
// tree, so a job blocked in e.g. `sleep 5` stops immediately, not five
// seconds (or however long is left) later.
func TestKillStopsJobImmediatelyEvenMidSleep(t *testing.T) {
	sh := New("posh")
	buf := &syncBuf{}
	sh.Stdout, sh.Stderr = buf, buf

	done := make(chan int, 1)
	go func() {
		done <- sh.EvalString("(while true; do sleep 5; done) &")
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("backgrounding statement should return immediately")
	}

	var jobs []*Job
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		jobs = sh.jobs.list()
		if len(jobs) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs.list() = %d entries, want 1", len(jobs))
	}
	job := jobs[0]

	// Give the job a moment to actually get into the sleep 5 call before
	// killing it, so this test exercises the "blocked mid-command" case
	// rather than possibly landing between iterations by luck.
	time.Sleep(300 * time.Millisecond)

	start := time.Now()
	if err := job.RequestStop(os.Kill); err != nil {
		t.Fatalf("RequestStop: %v", err)
	}
	select {
	case <-job.done:
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("job took %v to stop after RequestStop -- it waited for the sleep instead of being killed immediately", elapsed)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("job did not stop within 4s of RequestStop -- it waited for sleep 5 to finish naturally")
	}
}

// TestBackgroundedPlainCommandStillUsesRealProcess ensures the fix for the
// subshell case above did not regress the pre-existing, working case: a plain
// backgrounded external command must still detach as a real OS process (a
// real PID, real signal delivery) rather than falling into the generic
// goroutine-job path. This guards against the exact regression hit while
// developing the fix: the parser always wraps a plain command in a
// single-Cmd *Pipeline (see parsePipeline), so evalNode must unwrap that
// before deciding a node is "just a plain command".
func TestBackgroundedPlainCommandStillUsesRealProcess(t *testing.T) {
	sh := New("posh")
	buf := &syncBuf{}
	sh.Stdout, sh.Stderr = buf, buf

	done := make(chan int, 1)
	go func() { done <- sh.EvalString("sleep 2 &") }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("backgrounding statement should return immediately")
	}

	var jobs []*Job
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		jobs = sh.jobs.list()
		if len(jobs) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs.list() = %d entries, want 1", len(jobs))
	}
	if !jobs[0].IsProcess() {
		t.Fatal("a backgrounded plain external command must be a real process job, not a goroutine job")
	}
	jobs[0].RequestStop(os.Kill) // clean up rather than waiting out the full sleep
}
