//go:build darwin

package gops

import (
	"context"
	"os/exec"

	"github.com/hightemp/smpmon/internal/collect"
)

// readBatteries parses `pmset -g batt`. It runs rarely (every 30 s), so a
// child process is cheaper than binding IOKit's power source API.
func readBatteries(ctx context.Context) ([]collect.Battery, error) {
	out, err := exec.CommandContext(ctx, "pmset", "-g", "batt").Output()
	if err != nil {
		return nil, err
	}
	return parsePmset(string(out)), nil
}
