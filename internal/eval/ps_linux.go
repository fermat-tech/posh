//go:build linux

package eval

import (
	"fmt"
	"io"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"
)

type procInfo struct {
	pid   uint32
	ppid  uint32
	name  string
	uid   string
	stime time.Time
	cpu   time.Duration
	cmd   string
}

func listProcesses() ([]procInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var procs []procInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid64, err := strconv.ParseUint(e.Name(), 10, 32)
		if err != nil {
			continue
		}
		pid := uint32(pid64)
		base := fmt.Sprintf("/proc/%d", pid)

		statData, err := os.ReadFile(base + "/stat")
		if err != nil {
			continue
		}
		s := string(statData)
		nameStart := strings.Index(s, "(")
		nameEnd := strings.LastIndex(s, ")")
		if nameStart < 0 || nameEnd < 0 {
			continue
		}
		name := s[nameStart+1 : nameEnd]
		fields := strings.Fields(s[nameEnd+2:])
		var ppid uint32
		if len(fields) > 1 {
			p, _ := strconv.ParseUint(fields[1], 10, 32)
			ppid = uint32(p)
		}
		// fields[19] = starttime in clock ticks (after state, ppid, pgrp, session, tty, tpgid, flags, minflt, cminflt, majflt, cmajflt, utime, stime, cutime, cstime, priority, nice, num_threads, itrealvalue)
		// utime=fields[11], stime=fields[12]
		var cpuTicks uint64
		if len(fields) > 13 {
			u, _ := strconv.ParseUint(fields[11], 10, 64)
			sv, _ := strconv.ParseUint(fields[12], 10, 64)
			cpuTicks = u + sv
		}
		cpu := time.Duration(cpuTicks) * time.Second / 100 // assume HZ=100

		// Full command line
		cmdlineData, _ := os.ReadFile(base + "/cmdline")
		cmd := ""
		if len(cmdlineData) > 0 {
			cmd = strings.TrimRight(strings.ReplaceAll(string(cmdlineData), "\x00", " "), " ")
		}
		if cmd == "" {
			cmd = "[" + name + "]"
		}

		// UID
		uid := "?"
		statusData, _ := os.ReadFile(base + "/status")
		for _, line := range strings.SplitN(string(statusData), "\n", 50) {
			if strings.HasPrefix(line, "Uid:") {
				if f := strings.Fields(line); len(f) > 1 {
					if u, err := user.LookupId(f[1]); err == nil {
						uid = u.Username
					} else {
						uid = f[1]
					}
				}
				break
			}
		}

		procs = append(procs, procInfo{
			pid:  pid,
			ppid: ppid,
			name: name,
			uid:  uid,
			cpu:  cpu,
			cmd:  cmd,
		})
	}
	return procs, nil
}

func builtinPs(_ *Shell, args []string, _ io.Reader, stdout, stderr io.Writer) int {
	full := false
	filterPIDs := map[uint32]bool{}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-e", "-A":
		case "-f":
			full = true
		case "-p":
			i++
			if i < len(args) {
				var pid uint32
				fmt.Sscanf(args[i], "%d", &pid)
				filterPIDs[pid] = true
			}
		default:
			for _, ch := range args[i] {
				if ch == 'f' {
					full = true
				}
			}
		}
	}

	procs, err := listProcesses()
	if err != nil {
		fmt.Fprintf(stderr, "ps: %v\n", err)
		return 1
	}

	now := time.Now()
	if full {
		fmt.Fprintf(stdout, "%-16s %6s %6s  C %-5s %-8s %8s CMD\n",
			"UID", "PID", "PPID", "STIME", "TTY", "TIME")
		for _, p := range procs {
			if len(filterPIDs) > 0 && !filterPIDs[p.pid] {
				continue
			}
			fmt.Fprintf(stdout, "%-16s %6d %6d  0 %-5s %-8s %8s %s\n",
				p.uid, p.pid, p.ppid, formatStime(p.stime, now), "?",
				formatCPU(p.cpu), p.cmd)
		}
	} else {
		fmt.Fprintf(stdout, "%6s  COMMAND\n", "PID")
		for _, p := range procs {
			if len(filterPIDs) > 0 && !filterPIDs[p.pid] {
				continue
			}
			fmt.Fprintf(stdout, "%6d  %s\n", p.pid, p.cmd)
		}
	}
	return 0
}

func formatStime(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "?"
	}
	if now.Sub(t) < 24*time.Hour && t.Day() == now.Day() {
		return t.Format("15:04")
	}
	return t.Format("Jan02")
}

func formatCPU(d time.Duration) string {
	if d == 0 {
		return "00:00:00"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	sv := int(d.Seconds()) % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, sv)
}
