//go:build !windows

package remote

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hightemp/smpmon/internal/source"
)

// fakeSSH writes an "ssh" that runs the remote command locally with a clean
// PATH, and answers `uname -sm` from $FAKE_UNAME when set.
func fakeSSH(t *testing.T) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "ssh")
	body := `#!/bin/sh
while [ $# -gt 2 ]; do shift; done
target="$1"; cmd="$2"
if [ "$target" = "unreachable" ]; then echo "ssh: connect to host unreachable port 22: Connection refused" >&2; exit 255; fi
if [ "$cmd" = "uname -sm" ] && [ -n "$FAKE_UNAME" ]; then echo "$FAKE_UNAME"; exit 0; fi
exec env PATH=/usr/bin:/bin sh -c "$cmd"
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// buildSmpmon builds the real binary into dir.
func buildSmpmon(t *testing.T, dir string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds smpmon")
	}
	out := filepath.Join(dir, "smpmon")
	cmd := exec.Command("go", "build", "-o", out, "github.com/hightemp/smpmon/cmd/smpmon")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, b)
	}
	return out
}

func TestSSHEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	buildSmpmon(t, binDir)
	ssh := fakeSSH(t)

	c := NewClient(ClientOptions{Name: "srv", Dial: SSHDialer(SSHOptions{Target: "srv", Binary: ssh}), Interval: 200 * time.Millisecond})
	c.SetDemand([]string{"cpu", "mem", "procs"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()
	waitFor(t, "online", func() bool { return c.Conn().State == source.Online })
	waitFor(t, "cpu over ssh", func() bool { return c.Store().Has("cpu.total.usage") })
	waitFor(t, "processes over ssh", func() bool { return c.Store().Has("proc.count") })
	if info := c.Conn(); info.OS != runtime.GOOS || info.Agent == "" {
		t.Fatalf("info %+v", info)
	}
}

func TestSSHErrors(t *testing.T) {
	home := t.TempDir() // no smpmon installed
	t.Setenv("HOME", home)
	ssh := fakeSSH(t)
	for target, want := range map[string]string{
		"srv":         "not installed",
		"unreachable": "Connection refused",
	} {
		c := NewClient(ClientOptions{Name: target, Dial: SSHDialer(SSHOptions{Target: target, Binary: ssh}), HelloTimeout: 5 * time.Second})
		ctx, cancel := context.WithCancel(context.Background())
		go func() { _ = c.Run(ctx) }()
		waitFor(t, target+" error", func() bool {
			err := c.Conn().Err
			return err != nil && strings.Contains(err.Error(), want)
		})
		cancel()
	}
}

func TestInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ssh := fakeSSH(t)
	fake := []byte("#!/bin/sh\necho \"smpmon fake $1\"\n")
	var log bytes.Buffer

	// Same platform: our own binary is copied.
	err := Install(context.Background(), InstallOptions{
		SSH: SSHOptions{Target: "srv", Binary: ssh}, Version: "1.2.3", Log: &log,
		self: func() ([]byte, error) { return fake, nil },
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	got, _ := os.ReadFile(filepath.Join(home, ".local/bin/smpmon"))
	if !bytes.Equal(got, fake) || !strings.Contains(log.String(), "smpmon fake version") {
		t.Fatalf("installed %q, log %s", got, log.String())
	}

	// Other platform: the release archive is downloaded and verified.
	other := "Linux aarch64"
	if runtime.GOARCH == "arm64" {
		other = "Linux x86_64"
	}
	t.Setenv("FAKE_UNAME", other)
	goarch := map[string]string{"Linux aarch64": "arm64", "Linux x86_64": "amd64"}[other]
	payload := []byte("#!/bin/sh\necho \"smpmon downloaded $1\"\n")
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "LICENSE", Mode: 0o644, Size: 2})
	_, _ = tw.Write([]byte("hi"))
	_ = tw.WriteHeader(&tar.Header{Name: "smpmon", Mode: 0o755, Size: int64(len(payload))})
	_, _ = tw.Write(payload)
	_ = tw.Close()
	_ = gz.Close()
	name := fmt.Sprintf("smpmon_2.0.0_linux_%s.tar.gz", goarch)
	sum := sha256.Sum256(archive.Bytes())
	corrupt := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			_, _ = w.Write([]byte(`{"tag_name":"v2.0.0"}`))
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			s := hex.EncodeToString(sum[:])
			if corrupt {
				s = strings.Repeat("0", 64)
			}
			_, _ = fmt.Fprintf(w, "%s  %s\n", s, name)
		case strings.HasSuffix(r.URL.Path, "/"+name):
			_, _ = w.Write(archive.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	opts := InstallOptions{SSH: SSHOptions{Target: "srv", Binary: ssh}, Version: "dev", Log: &log, BaseURL: srv.URL, APIURL: srv.URL}
	if err := Install(context.Background(), opts); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	got, _ = os.ReadFile(filepath.Join(home, ".local/bin/smpmon"))
	if !bytes.Equal(got, payload) {
		t.Fatalf("downloaded binary not installed: %q", got)
	}
	corrupt = true
	if err := Install(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("checksum mismatch not detected: %v", err)
	}
}

func TestPlatformFromUname(t *testing.T) {
	for in, want := range map[string]string{
		"Linux x86_64": "linux/amd64", "Linux aarch64": "linux/arm64", "Darwin arm64": "darwin/arm64",
	} {
		os, arch, err := platformFromUname(in)
		if err != nil || os+"/"+arch != want {
			t.Errorf("%q: %s/%s %v", in, os, arch, err)
		}
	}
	for _, bad := range []string{"", "FreeBSD amd64", "Linux mips"} {
		if _, _, err := platformFromUname(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
