// Package platform returns the collectors implemented for the current OS.
package platform

import (
	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/collect/linux"
)

// Collectors returns fresh instances of all collectors for this OS.
func Collectors() []collect.Collector {
	return linux.Collectors()
}

// ProcessController returns the process controller for this OS.
func ProcessController() collect.ProcessController {
	return linux.ProcessController{}
}
