//go:build windows

package procctl

import (
	"golang.org/x/sys/windows"

	"github.com/hightemp/smpmon/internal/collect"
)

type controller struct{}

const terminate = 1

// Signals implements collect.ProcessController. Windows has no signals;
// the only action is TerminateProcess.
func (controller) Signals() []collect.SignalInfo {
	return []collect.SignalInfo{{Name: "Terminate", Number: terminate}}
}

// Signal implements collect.ProcessController.
func (controller) Signal(pid int32, _ int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	return windows.TerminateProcess(h, 1)
}

// priorityClass maps a Unix nice value to the closest Windows priority
// class. Realtime is never used: it can starve the system.
func priorityClass(nice int) uint32 {
	switch {
	case nice <= -15:
		return windows.HIGH_PRIORITY_CLASS
	case nice < 0:
		return windows.ABOVE_NORMAL_PRIORITY_CLASS
	case nice == 0:
		return windows.NORMAL_PRIORITY_CLASS
	case nice <= 10:
		return windows.BELOW_NORMAL_PRIORITY_CLASS
	default:
		return windows.IDLE_PRIORITY_CLASS
	}
}

// SetNice implements collect.ProcessController.
func (controller) SetNice(pid int32, nice int) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_INFORMATION, false, uint32(pid))
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	return windows.SetPriorityClass(h, priorityClass(nice))
}
