package gops

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
)

// pmsetRe matches lines like
// " -InternalBattery-0 (id=4653155)	85%; discharging; 4:12 remaining present: true".
var pmsetRe = regexp.MustCompile(`-(\S+) \(id=\d+\)\s+(\d+)%; ([^;]+);\s*(?:(\d+):(\d+) remaining)?`)

// parsePmset parses `pmset -g batt` output. It is OS-independent so it can
// be tested everywhere.
func parsePmset(out string) []collect.Battery {
	var bats []collect.Battery
	for _, m := range pmsetRe.FindAllStringSubmatch(out, -1) {
		pct, _ := strconv.Atoi(m[2])
		b := collect.Battery{Name: m[1], Percent: float64(pct), Status: strings.TrimSpace(m[3])}
		if b.Status == "charged" || b.Status == "finishing charge" {
			b.Status = "full"
		}
		if m[4] != "" {
			h, _ := strconv.Atoi(m[4])
			mins, _ := strconv.Atoi(m[5])
			b.TimeLeft = time.Duration(h)*time.Hour + time.Duration(mins)*time.Minute
		}
		if b.Status == "full" {
			b.TimeLeft = 0
		}
		bats = append(bats, b)
	}
	return bats
}
