//go:build linux

package gpu

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeProc writes /proc/<pid>/fd links and fdinfo files.
type fakeProc struct {
	t    *testing.T
	root string
}

func (f fakeProc) fd(pid, fd int, target, info string) {
	f.t.Helper()
	dir := filepath.Join(f.root, fmt.Sprint(pid))
	for _, d := range []string{"fd", "fdinfo"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			f.t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "fd", fmt.Sprint(fd))
	_ = os.Remove(link)
	if err := os.Symlink(target, link); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fdinfo", fmt.Sprint(fd)), []byte(info), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func amdInfo(client int, gfx, enc int64, vram int) string {
	return fmt.Sprintf("pos:\t0\nflags:\t02100002\ndrm-driver:\tamdgpu\ndrm-client-id:\t%d\ndrm-pdev:\t0000:03:00.0\n"+
		"drm-memory-vram:\t%d KiB\ndrm-memory-gtt:\t2048 KiB\ndrm-engine-gfx:\t%d ns\ndrm-engine-compute:\t0 ns\ndrm-engine-enc:\t%d ns\n",
		client, vram, gfx, enc)
}

func xeInfo(client int, rcs, total int64) string {
	return fmt.Sprintf("drm-driver:\txe\ndrm-client-id:\t%d\ndrm-pdev:\t0000:00:02.0\ndrm-resident-system0:\t4096 KiB\n"+
		"drm-cycles-rcs:\t%d\ndrm-total-cycles-rcs:\t%d\ndrm-cycles-vcs:\t0\ndrm-total-cycles-vcs:\t%d\n", client, rcs, total, total)
}

func TestDRMProcesses(t *testing.T) {
	root := t.TempDir()
	p := fakeProc{t, root}
	p.fd(100, 5, "/dev/dri/renderD128", amdInfo(7, 0, 0, 1024))
	p.fd(100, 6, "/dev/dri/renderD128", amdInfo(7, 0, 0, 1024)) // same client via another fd
	p.fd(100, 1, "/dev/null", "pos:\t0\n")
	p.fd(200, 3, "/dev/dri/renderD129", xeInfo(9, 1000, 10000))
	p.fd(300, 4, "/dev/dri/renderD130", strings.Replace(amdInfo(1, 0, 0, 1), "0000:03:00.0", "0000:09:00.0", 1)) // unknown card
	d := &drm{proc: root, pci: map[string]int{"0000:03:00.0": 1, "0000:00:02.0": 2}, prev: map[drmClientID]drmClient{}}

	t0 := time.Unix(1000, 0)
	first := d.processes(t0)
	if len(first) != 2 || first[0].PID != 100 || first[0].GPU != 1 || first[0].MemUsed != 1<<20 || first[0].Util != -1 {
		t.Fatalf("first %+v", first)
	}
	if first[1].PID != 200 || first[1].GPU != 2 || first[1].MemUsed != 4<<20 { // integrated: system memory
		t.Fatalf("xe %+v", first[1])
	}

	// One second later: amdgpu gfx busy 250 ms, encoder 100 ms; xe rcs 3000 of 10000 cycles.
	p.fd(100, 5, "/dev/dri/renderD128", amdInfo(7, 250e6, 100e6, 2048))
	p.fd(200, 3, "/dev/dri/renderD129", xeInfo(9, 4000, 20000))
	second := d.processes(t0.Add(time.Second))
	amd, xe := second[0], second[1]
	if amd.Util != 25 || amd.Enc != 10 || amd.Dec != -1 || amd.MemUsed != 2<<20 || amd.Type != "G" {
		t.Fatalf("amd %+v", amd)
	}
	if xe.Util != 30 || xe.Dec != 0 {
		t.Fatalf("xe %+v", xe)
	}

	// An exited process disappears; a new one appears after the rescan.
	if err := os.RemoveAll(filepath.Join(root, "200")); err != nil {
		t.Fatal(err)
	}
	p.fd(400, 3, "/dev/dri/renderD128", amdInfo(11, 0, 0, 1))
	third := d.processes(t0.Add(2 * time.Second))
	if len(third) != 1 || third[0].PID != 100 {
		t.Fatalf("before rescan %+v", third)
	}
	fourth := d.processes(t0.Add(2*time.Second + rescanEvery))
	if len(fourth) != 2 || fourth[1].PID != 400 {
		t.Fatalf("after rescan %+v", fourth)
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]uint64{"12 KiB": 12 << 10, "3 MiB": 3 << 20, "100": 100, "": 0, "x KiB": 0} {
		if got := parseSize(in); got != want {
			t.Errorf("%q: %d, want %d", in, got, want)
		}
	}
}
