//go:build windows

package eval

import (
	"fmt"
	"io"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modKernel32ps            = syscall.NewLazyDLL("kernel32.dll")
	procCreateToolhelp32Snap = modKernel32ps.NewProc("CreateToolhelp32Snapshot")
	procProcess32First       = modKernel32ps.NewProc("Process32FirstW")
	procProcess32Next        = modKernel32ps.NewProc("Process32NextW")
)

const th32csSnapProcess = 0x00000002

type processEntry32 struct {
	dwSize              uint32
	cntUsage            uint32
	th32ProcessID       uint32
	th32DefaultHeapID   uintptr
	th32ModuleID        uint32
	cntThreads          uint32
	th32ParentProcessID uint32
	pcPriClassBase      int32
	dwFlags             uint32
	szExeFile           [260]uint16
}

type procInfo struct {
	pid   uint32
	ppid  uint32
	name  string
	uid   string
	stime time.Time
	cpu   time.Duration // kernel + user time
}

func listProcesses() ([]procInfo, error) {
	snap, _, err := procCreateToolhelp32Snap.Call(th32csSnapProcess, 0)
	if snap == ^uintptr(0) {
		return nil, fmt.Errorf("CreateToolhelp32Snapshot: %v", err)
	}
	defer windows.CloseHandle(windows.Handle(snap))

	var entry processEntry32
	entry.dwSize = uint32(unsafe.Sizeof(entry))

	ret, _, _ := procProcess32First.Call(snap, uintptr(unsafe.Pointer(&entry)))
	if ret == 0 {
		return nil, nil
	}

	var procs []procInfo
	for {
		p := procInfo{
			pid:  entry.th32ProcessID,
			ppid: entry.th32ParentProcessID,
			name: syscall.UTF16ToString(entry.szExeFile[:]),
		}
		enrichProcess(&p)
		procs = append(procs, p)

		entry.dwSize = uint32(unsafe.Sizeof(entry))
		ret, _, _ = procProcess32Next.Call(snap, uintptr(unsafe.Pointer(&entry)))
		if ret == 0 {
			break
		}
	}
	return procs, nil
}

// enrichProcess fills uid, stime, and cpu for a process by opening its handle.
// Failures are silently ignored — the fields stay at zero values.
func enrichProcess(p *procInfo) {
	const access = windows.PROCESS_QUERY_LIMITED_INFORMATION
	h, err := windows.OpenProcess(access, false, p.pid)
	if err != nil {
		p.uid = "?"
		return
	}
	defer windows.CloseHandle(h)

	// Owner username via process token
	var tok windows.Token
	if windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok) == nil {
		if u, err := tok.GetTokenUser(); err == nil {
			account, domain, _, err := u.User.Sid.LookupAccount("")
			if err == nil {
				_ = domain
				p.uid = account
			}
		}
		tok.Close()
	}
	if p.uid == "" {
		p.uid = "?"
	}

	// Process times
	var creation, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &creation, &exit, &kernel, &user) == nil {
		p.stime = time.Unix(0, creation.Nanoseconds())
		kns := (int64(kernel.HighDateTime)<<32 | int64(kernel.LowDateTime)) * 100
		uns := (int64(user.HighDateTime)<<32 | int64(user.LowDateTime)) * 100
		p.cpu = time.Duration(kns + uns)
	}
}

func builtinPs(_ *Shell, args []string, _ io.Reader, stdout, stderr io.Writer) int {
	full := false
	filterPIDs := map[uint32]bool{}

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-e", "-A":
			// all processes — default on Windows
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
			// combined flags: -ef, -Af, aux, etc.
			for _, ch := range a {
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
			stime := formatStime(p.stime, now)
			cputime := formatCPU(p.cpu)
			fmt.Fprintf(stdout, "%-16s %6d %6d  0 %-5s %-8s %8s %s\n",
				p.uid, p.pid, p.ppid, stime, "?", cputime, p.name)
		}
	} else {
		fmt.Fprintf(stdout, "%6s  COMMAND\n", "PID")
		for _, p := range procs {
			if len(filterPIDs) > 0 && !filterPIDs[p.pid] {
				continue
			}
			fmt.Fprintf(stdout, "%6d  %s\n", p.pid, p.name)
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
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}
