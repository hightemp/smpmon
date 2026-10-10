//go:build !linux

// Package platform returns the collectors implemented for the current OS.
package platform

import "github.com/hightemp/smpmon/internal/collect"

// Collectors returns no collectors until macOS and Windows support lands.
func Collectors() []collect.Collector {
	return nil
}

// ProcessController returns nil until macOS and Windows support lands.
func ProcessController() collect.ProcessController {
	return nil
}
