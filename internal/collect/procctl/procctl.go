// Package procctl implements collect.ProcessController for each OS.
package procctl

import "github.com/hightemp/smpmon/internal/collect"

// New returns the process controller for this OS.
func New() collect.ProcessController { return controller{} }
