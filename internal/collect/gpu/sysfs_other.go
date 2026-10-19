//go:build !linux

package gpu

import (
	"time"

	"github.com/hightemp/smpmon/internal/collect"
)

type sysfsReader interface {
	read(firstIndex int) ([]collect.GPU, error)
}

func newSysfs() sysfsReader { return nil }

type drmReader interface {
	processes(now time.Time) []collect.GPUProcess
}

func newDRM(sysfsReader, int) drmReader { return nil }
