//go:build !linux

package gpu

import "os/exec"

// dieWithParent is a no-op: elsewhere the child exits on SIGPIPE at its next
// write once smpmon is gone.
func dieWithParent(*exec.Cmd) {}
