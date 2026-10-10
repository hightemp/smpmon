//go:build !linux

// Package platform returns the collectors implemented for the current OS.
package platform

import (
	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/collect/docker"
	"github.com/hightemp/smpmon/internal/collect/gops"
	"github.com/hightemp/smpmon/internal/collect/gpu"
	"github.com/hightemp/smpmon/internal/collect/procctl"
)

// Collectors returns fresh instances of all collectors for this OS.
func Collectors() []collect.Collector {
	return append(gops.Collectors(), gpu.New(), docker.New())
}

// ProcessController returns the process controller for this OS.
func ProcessController() collect.ProcessController {
	return procctl.New()
}
