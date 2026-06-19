//go:build !windows

package eval

import "os/exec"

func setForegroundAttrs(_ *exec.Cmd) {}
func sendInterrupt(pid int)          {}
