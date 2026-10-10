//go:build windows

package gops

import (
	"context"
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// gopsutil opens every process several times per sample on Windows.
// NtQuerySystemInformation(SystemProcessInformation) returns the whole
// process table, with times, memory and I/O counters, in one call.

// fileTimeEpoch is 1970-01-01 in FILETIME units (100ns since 1601).
const fileTimeEpoch = 116444736000000000

// windowsNice maps a base priority to the Unix nice scale used in the UI
// (see procctl's inverse mapping).
func windowsNice(prio int32) int32 {
	switch {
	case prio <= 4:
		return 19
	case prio <= 6:
		return 10
	case prio <= 9:
		return 0
	case prio <= 12:
		return -5
	case prio <= 15:
		return -15
	}
	return -20
}

func listWindows(ctx context.Context) ([]rawProc, error) {
	size := uint32(512 * 1024)
	var buf []byte
	for {
		buf = make([]byte, size)
		var need uint32
		err := windows.NtQuerySystemInformation(windows.SystemProcessInformation, unsafe.Pointer(&buf[0]), size, &need)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.STATUS_INFO_LENGTH_MISMATCH) || size > 64<<20 {
			return nil, err
		}
		size = max(need+64*1024, size*2) // the table may grow between calls
	}

	var out []rawProc
	for off := uint32(0); ; {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		e := (*windows.SYSTEM_PROCESS_INFORMATION)(unsafe.Pointer(&buf[off]))
		r := rawProc{
			pid:        int32(e.UniqueProcessID),
			ppid:       int32(e.InheritedFromUniqueProcessID),
			name:       e.ImageName.String(),
			nice:       windowsNice(e.BasePriority),
			state:      'S',
			threads:    int32(e.NumberOfThreads),
			rss:        uint64(e.WorkingSetSize),
			vms:        uint64(e.PrivatePageCount),
			cpuSeconds: float64(e.UserTime+e.KernelTime) / 1e7,
			hasTask:    true,
			readBytes:  uint64(e.ReadTransferCount),
			writeBytes: uint64(e.WriteTransferCount),
			hasIO:      true,
		}
		if e.CreateTime > fileTimeEpoch {
			r.created = (e.CreateTime - fileTimeEpoch) / 10000
		}
		switch r.pid {
		case 0:
			r.name = "System Idle Process"
		case 4:
			if r.name == "" {
				r.name = "System"
			}
		}
		out = append(out, r)
		if e.NextEntryOffset == 0 {
			break
		}
		off += e.NextEntryOffset
		if int(off) >= len(buf) {
			break
		}
	}
	return out, nil
}

func init() { fastList = listWindows }
