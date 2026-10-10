//go:build linux

package gpu

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/hightemp/smpmon/internal/collect"
)

type sysfsReader interface {
	read(firstIndex int) ([]collect.GPU, error)
}

// sysfs reads AMD (amdgpu) and Intel (i915/xe) cards from /sys/class/drm.
// NVIDIA cards are skipped: nvidia-smi covers them.
type sysfs struct {
	root  string // /sys
	cards []card
}

type card struct {
	dir    string // /sys/class/drm/cardN/device
	drm    string // /sys/class/drm/cardN
	vendor string
	name   string
	hwmon  string // hwmon dir with temp/power/fan, may be empty
}

var cardRe = regexp.MustCompile(`^card[0-9]+$`)

const (
	vendorAMD    = "0x1002"
	vendorIntel  = "0x8086"
	vendorNVIDIA = "0x10de"
)

func newSysfs() sysfsReader {
	if s := scanSysfs("/sys"); s != nil {
		return s
	}
	return nil // untyped nil: the interface must compare equal to nil
}

func readStr(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// readNum reads an integer file, returning -1 if missing.
func readNum(p string) float64 {
	v, err := strconv.ParseFloat(readStr(p), 64)
	if err != nil {
		return -1
	}
	return v
}

func scanSysfs(root string) *sysfs {
	base := filepath.Join(root, "class/drm")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	s := &sysfs{root: root}
	for _, e := range entries {
		if !cardRe.MatchString(e.Name()) {
			continue
		}
		drm := filepath.Join(base, e.Name())
		dev := filepath.Join(drm, "device")
		vendor := readStr(filepath.Join(dev, "vendor"))
		if vendor != vendorAMD && vendor != vendorIntel {
			continue
		}
		c := card{dir: dev, drm: drm, vendor: vendor}
		c.name = readStr(filepath.Join(dev, "product_name"))
		if c.name == "" {
			c.name = map[string]string{vendorAMD: "AMD GPU", vendorIntel: "Intel GPU"}[vendor]
		}
		if hw, _ := filepath.Glob(filepath.Join(dev, "hwmon", "hwmon*")); len(hw) > 0 {
			c.hwmon = hw[0]
		}
		s.cards = append(s.cards, c)
	}
	if len(s.cards) == 0 {
		return nil
	}
	slices.SortFunc(s.cards, func(a, b card) int { return strings.Compare(a.drm, b.drm) })
	return s
}

func (s *sysfs) read(first int) ([]collect.GPU, error) {
	out := make([]collect.GPU, 0, len(s.cards))
	for i, c := range s.cards {
		g := collect.GPU{
			Index: first + i, Name: c.name, Util: -1, MemUtil: -1, Temp: -1,
			Power: -1, PowerLimit: -1, Clock: -1, MemClock: -1, Fan: -1,
		}
		switch c.vendor {
		case vendorAMD:
			g.Vendor = "amd"
			g.Util = readNum(filepath.Join(c.dir, "gpu_busy_percent"))
			g.MemUtil = readNum(filepath.Join(c.dir, "mem_busy_percent"))
			if v := readNum(filepath.Join(c.dir, "mem_info_vram_used")); v >= 0 {
				g.MemUsed = uint64(v)
			}
			if v := readNum(filepath.Join(c.dir, "mem_info_vram_total")); v >= 0 {
				g.MemTotal = uint64(v)
			}
		case vendorIntel:
			g.Vendor = "intel"
			if v := readNum(filepath.Join(c.drm, "gt_cur_freq_mhz")); v >= 0 {
				g.Clock = v * 1e6
			} else if v := readNum(filepath.Join(c.dir, "tile0/gt0/freq0/cur_freq")); v >= 0 {
				g.Clock = v * 1e6 // xe driver
			}
		}
		if c.hwmon != "" {
			if v := readNum(filepath.Join(c.hwmon, "temp1_input")); v >= 0 {
				g.Temp = v / 1000
			}
			power := readNum(filepath.Join(c.hwmon, "power1_average"))
			if power < 0 {
				power = readNum(filepath.Join(c.hwmon, "power1_input"))
			}
			if power >= 0 {
				g.Power = power / 1e6
			}
			if v := readNum(filepath.Join(c.hwmon, "power1_cap")); v > 0 {
				g.PowerLimit = v / 1e6
			}
			if v := readNum(filepath.Join(c.hwmon, "freq1_input")); v >= 0 && g.Clock < 0 {
				g.Clock = v
			}
			if v := readNum(filepath.Join(c.hwmon, "freq2_input")); v >= 0 {
				g.MemClock = v
			}
			if pwm := readNum(filepath.Join(c.hwmon, "pwm1")); pwm >= 0 {
				g.Fan = pwm / 255 * 100
			}
		}
		out = append(out, g)
	}
	if len(out) == 0 {
		return nil, errors.New("no sysfs GPUs")
	}
	return out, nil
}
