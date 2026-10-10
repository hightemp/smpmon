package metrics

import (
	"fmt"
	"math"
	"strconv"
	"time"
)

var iecUnits = [...]string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}

// FormatBytes formats a byte count with IEC units, e.g. "1.5 GiB".
func FormatBytes(v float64) string {
	return scaled(v, 1024, iecUnits[:])
}

// FormatRate formats a byte rate, e.g. "34.5 MiB/s".
func FormatRate(v float64) string {
	return FormatBytes(v) + "/s"
}

var siUnits = [...]string{"", "k", "M", "G", "T", "P", "E"}

// FormatSI formats a value with SI prefixes, e.g. 2.4e9 -> "2.40 G".
func FormatSI(v float64) string {
	return scaled(v, 1000, siUnits[:])
}

var hzUnits = [...]string{"Hz", "kHz", "MHz", "GHz", "THz"}

// FormatHertz formats a frequency, e.g. "3.20 GHz".
func FormatHertz(v float64) string {
	return scaled(v, 1000, hzUnits[:])
}

// FormatPercent formats a percentage with one decimal, e.g. "42.0%".
func FormatPercent(v float64) string {
	return strconv.FormatFloat(v, 'f', 1, 64) + "%"
}

// FormatDuration formats seconds compactly, e.g. "3d 04:05:06" or "04:05".
func FormatDuration(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	if d < 0 {
		d = 0
	}
	days := int(d / (24 * time.Hour))
	h := int(d/time.Hour) % 24
	m := int(d/time.Minute) % 60
	s := int(d/time.Second) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %02d:%02d:%02d", days, h, m, s)
	case h > 0:
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	default:
		return fmt.Sprintf("%02d:%02d", m, s)
	}
}

// Format formats v according to unit.
func Format(v float64, u Unit) string {
	if math.IsNaN(v) {
		return "n/a"
	}
	switch u {
	case UnitPercent:
		return FormatPercent(v)
	case UnitBytes:
		return FormatBytes(v)
	case UnitBytesPerSec:
		return FormatRate(v)
	case UnitCelsius:
		return strconv.FormatFloat(v, 'f', 0, 64) + "°C"
	case UnitHertz:
		return FormatHertz(v)
	case UnitSeconds:
		return FormatDuration(v)
	case UnitPerSec:
		return FormatSI(v) + "/s"
	case UnitWatts:
		return strconv.FormatFloat(v, 'f', 1, 64) + " W"
	case UnitRPM:
		return strconv.FormatFloat(v, 'f', 0, 64) + " RPM"
	case UnitCount:
		return FormatSI(v)
	default:
		return strconv.FormatFloat(v, 'g', 4, 64)
	}
}

// scaled divides v by base until it fits under base and appends the unit.
// Precision shrinks as the integer part grows so results stay ~4 characters.
func scaled(v float64, base float64, units []string) string {
	neg := v < 0
	if neg {
		v = -v
	}
	i := 0
	for v >= base && i < len(units)-1 {
		v /= base
		i++
	}
	var num string
	switch {
	case i == 0 && v == math.Trunc(v):
		num = strconv.FormatFloat(v, 'f', 0, 64)
	case v >= 100:
		num = strconv.FormatFloat(v, 'f', 0, 64)
	case v >= 10:
		num = strconv.FormatFloat(v, 'f', 1, 64)
	default:
		num = strconv.FormatFloat(v, 'f', 2, 64)
	}
	if neg {
		num = "-" + num
	}
	if units[i] == "" {
		return num
	}
	return num + " " + units[i]
}
