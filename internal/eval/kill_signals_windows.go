//go:build windows

package eval

import "os"

// Windows only reliably delivers os.Kill (TerminateProcess) and os.Interrupt
// (Ctrl+C event); everything else is mapped to one of those two.
var killSignals = map[string]os.Signal{
	"1": os.Kill, "HUP": os.Kill,
	"2": os.Interrupt, "INT": os.Interrupt,
	"3": os.Kill, "QUIT": os.Kill,
	"6": os.Kill, "ABRT": os.Kill,
	"9": os.Kill, "KILL": os.Kill,
	"15": os.Kill, "TERM": os.Kill,
	"19": os.Kill, "STOP": os.Kill,
}

var killSignalList = [][2]string{
	{"1", "HUP"}, {"2", "INT"}, {"3", "QUIT"}, {"4", "ILL"},
	{"5", "TRAP"}, {"6", "ABRT"}, {"8", "FPE"}, {"9", "KILL"},
	{"11", "SEGV"}, {"13", "PIPE"}, {"14", "ALRM"}, {"15", "TERM"},
	{"17", "CHLD"}, {"18", "CONT"}, {"19", "STOP"}, {"20", "TSTP"},
	{"21", "TTIN"}, {"22", "TTOU"},
}
