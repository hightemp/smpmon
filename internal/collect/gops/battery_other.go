//go:build !darwin && !windows

package gops

import (
	"context"

	"github.com/hightemp/smpmon/internal/collect"
)

// readBatteries reports no batteries; Init turns that into ErrUnsupported.
func readBatteries(context.Context) ([]collect.Battery, error) {
	return nil, nil
}
