//go:build linux

package gpu

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSysfsAMD(t *testing.T) {
	root := t.TempDir()
	w := func(rel, v string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dev := "class/drm/card0/device/"
	w(dev+"vendor", "0x1002")
	w(dev+"product_name", "Radeon RX 7800 XT")
	w(dev+"gpu_busy_percent", "42")
	w(dev+"mem_info_vram_used", "1073741824")
	w(dev+"mem_info_vram_total", "17179869184")
	w(dev+"hwmon/hwmon5/temp1_input", "55000")
	w(dev+"hwmon/hwmon5/power1_average", "120000000")
	w(dev+"hwmon/hwmon5/pwm1", "128")
	w("class/drm/card1/device/vendor", "0x10de") // NVIDIA: skipped
	w("class/drm/card0-DP-1/status", "connected")

	s := scanSysfs(root)
	if s == nil || len(s.cards) != 1 {
		t.Fatalf("cards %+v", s)
	}
	gpus, err := s.read(1)
	if err != nil {
		t.Fatal(err)
	}
	g := gpus[0]
	if g.Index != 1 || g.Vendor != "amd" || g.Name != "Radeon RX 7800 XT" || g.Util != 42 ||
		g.MemUsed != 1<<30 || g.Temp != 55 || g.Power != 120 || g.Fan < 50 || g.Fan > 51 {
		t.Fatalf("gpu %+v", g)
	}
	if scanSysfs(t.TempDir()) != nil {
		t.Fatal("empty tree should have no cards")
	}
}
