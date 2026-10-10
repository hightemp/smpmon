//go:build !linux

package gpu

import "github.com/hightemp/smpmon/internal/collect"

type sysfsReader interface {
	read(firstIndex int) ([]collect.GPU, error)
}

func newSysfs() sysfsReader { return nil }
