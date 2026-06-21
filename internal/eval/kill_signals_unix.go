//go:build !windows

package eval

import (
	"os"
	"syscall"
)

var killSignals = map[string]os.Signal{
	"1": syscall.SIGHUP, "HUP": syscall.SIGHUP,
	"2": syscall.SIGINT, "INT": syscall.SIGINT,
	"3": syscall.SIGQUIT, "QUIT": syscall.SIGQUIT,
	"6": syscall.SIGABRT, "ABRT": syscall.SIGABRT,
	"9": syscall.SIGKILL, "KILL": syscall.SIGKILL,
	"15": syscall.SIGTERM, "TERM": syscall.SIGTERM,
	"18": syscall.SIGCONT, "CONT": syscall.SIGCONT,
	"19": syscall.SIGSTOP, "STOP": syscall.SIGSTOP,
	"20": syscall.SIGTSTP, "TSTP": syscall.SIGTSTP,
}

var killSignalList = [][2]string{
	{"1", "HUP"}, {"2", "INT"}, {"3", "QUIT"}, {"4", "ILL"},
	{"5", "TRAP"}, {"6", "ABRT"}, {"8", "FPE"}, {"9", "KILL"},
	{"11", "SEGV"}, {"13", "PIPE"}, {"14", "ALRM"}, {"15", "TERM"},
	{"17", "CHLD"}, {"18", "CONT"}, {"19", "STOP"}, {"20", "TSTP"},
	{"21", "TTIN"}, {"22", "TTOU"},
}
