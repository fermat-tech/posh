package eval

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

// Job tracks a background job. It is one of two kinds:
//
//   - A real OS process: a single external command run directly in the
//     background (e.g. `sleep 5 &`). Cmd is set; PID/Wait/signal all go
//     through it directly.
//   - A goroutine job: a backgrounded compound command (a subshell, loop,
//     group, pipeline, ...) that runs as a plain Go goroutine within posh's own
//     process, since none of those constructs spawn an OS process by
//     themselves. Cmd is nil; done/kill stand in for Wait/signal. There is no
//     real PID to report or send a Unix signal to, so `kill %n` on this kind
//     always just asks the goroutine to stop (via the same interrupt mechanism
//     Ctrl+C uses — see checkInterrupt), regardless of which signal was named.
type Job struct {
	ID   int
	Desc string

	Cmd *exec.Cmd // set for a process-backed job; nil for a goroutine job

	done chan struct{} // goroutine job only: closed when it finishes
	kill *int32        // goroutine job only: set to 1 to request it stop
}

// IsProcess reports whether j is backed by a real OS process (as opposed to a
// backgrounded compound command running as a goroutine within posh itself).
func (j *Job) IsProcess() bool { return j.Cmd != nil }

// Wait blocks until the job finishes.
func (j *Job) Wait() {
	if j.Cmd != nil {
		j.Cmd.Wait()
		return
	}
	<-j.done
}

// RequestStop asks the job to stop. For a process-backed job this sends sig;
// for a goroutine job there is no real signal delivery available, so it always
// just requests the job's loop/list unwind at its next check, matching Ctrl+C.
func (j *Job) RequestStop(sig os.Signal) error {
	if j.Cmd != nil {
		if err := killProcessTree(j.Cmd.Process.Pid); err == nil {
			return nil
		}
		return j.Cmd.Process.Signal(sig)
	}
	atomic.StoreInt32(j.kill, 1)
	return nil
}

// JobTable manages background jobs.
type JobTable struct {
	mu   sync.Mutex
	jobs []*Job
}

func newJobTable() *JobTable { return &JobTable{} }

// nextID returns the lowest positive integer not already in use.
func (jt *JobTable) nextID() int {
	for id := 1; ; id++ {
		used := false
		for _, j := range jt.jobs {
			if j.ID == id {
				used = true
				break
			}
		}
		if !used {
			return id
		}
	}
}

func (jt *JobTable) add(cmd *exec.Cmd, desc string) *Job {
	jt.mu.Lock()
	defer jt.mu.Unlock()
	j := &Job{ID: jt.nextID(), Cmd: cmd, Desc: desc}
	jt.jobs = append(jt.jobs, j)
	fmt.Fprintf(os.Stderr, "[%d] %d\n", j.ID, cmd.Process.Pid)
	// Reap in background
	go func() {
		j.Cmd.Wait()
		jt.remove(j)
		fmt.Fprintf(os.Stderr, "[%d] Done\t%s\n", j.ID, j.Desc)
	}()
	return j
}

// addGoroutine registers a backgrounded compound command that runs as a
// goroutine rather than a real OS process (see Job). The caller (evalNode)
// runs the job's body and calls finish() when it completes, which closes
// done and reaps the table entry — mirroring add's process-based reaper.
func (jt *JobTable) addGoroutine(desc string) (j *Job, finish func()) {
	jt.mu.Lock()
	defer jt.mu.Unlock()
	kill := new(int32)
	j = &Job{ID: jt.nextID(), Desc: desc, done: make(chan struct{}), kill: kill}
	jt.jobs = append(jt.jobs, j)
	fmt.Fprintf(os.Stderr, "[%d] %s\n", j.ID, desc)
	return j, func() {
		close(j.done)
		jt.remove(j)
		fmt.Fprintf(os.Stderr, "[%d] Done\t%s\n", j.ID, j.Desc)
	}
}

func (jt *JobTable) remove(j *Job) {
	jt.mu.Lock()
	defer jt.mu.Unlock()
	for i, jj := range jt.jobs {
		if jj == j {
			jt.jobs = append(jt.jobs[:i], jt.jobs[i+1:]...)
			break
		}
	}
}

func (jt *JobTable) list() []*Job {
	jt.mu.Lock()
	defer jt.mu.Unlock()
	out := make([]*Job, len(jt.jobs))
	copy(out, jt.jobs)
	return out
}
