//go:build linux

package gpu

import (
	"os/exec"
	"syscall"
)

// dieWithParent makes the kernel kill the child if smpmon dies without
// cleaning up (crash, SIGKILL).
func dieWithParent(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
