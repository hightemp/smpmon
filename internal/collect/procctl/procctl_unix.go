//go:build unix

package procctl

import (
	"syscall"

	"github.com/hightemp/smpmon/internal/collect"
)

type controller struct{}

// Signals implements collect.ProcessController.
func (controller) Signals() []collect.SignalInfo {
	return []collect.SignalInfo{
		{Name: "SIGTERM", Number: int(syscall.SIGTERM)},
		{Name: "SIGKILL", Number: int(syscall.SIGKILL)},
		{Name: "SIGINT", Number: int(syscall.SIGINT)},
		{Name: "SIGHUP", Number: int(syscall.SIGHUP)},
		{Name: "SIGQUIT", Number: int(syscall.SIGQUIT)},
		{Name: "SIGSTOP", Number: int(syscall.SIGSTOP)},
		{Name: "SIGCONT", Number: int(syscall.SIGCONT)},
		{Name: "SIGUSR1", Number: int(syscall.SIGUSR1)},
		{Name: "SIGUSR2", Number: int(syscall.SIGUSR2)},
	}
}

// Signal implements collect.ProcessController.
func (controller) Signal(pid int32, sig int) error {
	return syscall.Kill(int(pid), syscall.Signal(sig))
}

// SetNice implements collect.ProcessController.
func (controller) SetNice(pid int32, nice int) error {
	return syscall.Setpriority(syscall.PRIO_PROCESS, int(pid), nice)
}
