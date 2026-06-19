package eval

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
)

// Job tracks a background process.
type Job struct {
	ID  int
	Cmd *exec.Cmd
	Desc string
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
		jt.mu.Lock()
		for i, jj := range jt.jobs {
			if jj == j {
				jt.jobs = append(jt.jobs[:i], jt.jobs[i+1:]...)
				break
			}
		}
		jt.mu.Unlock()
		fmt.Fprintf(os.Stderr, "[%d] Done\t%s\n", j.ID, j.Desc)
	}()
	return j
}

func (jt *JobTable) list() []*Job {
	jt.mu.Lock()
	defer jt.mu.Unlock()
	out := make([]*Job, len(jt.jobs))
	copy(out, jt.jobs)
	return out
}
