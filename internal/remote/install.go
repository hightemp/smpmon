package remote

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
)

// InstallOptions configures `connect --install`.
type InstallOptions struct {
	SSH SSHOptions
	// Version is the local smpmon version; a release with the same
	// version is installed, or the latest release for development builds.
	Version string
	// Repo is the GitHub repository with releases.
	Repo string
	// Log receives progress messages.
	Log io.Writer
	// HTTP is used for downloads (tests replace it).
	HTTP *http.Client
	// BaseURL overrides https://github.com and https://api.github.com in tests.
	BaseURL, APIURL string
	// self replaces os.Executable's contents in tests.
	self func() ([]byte, error)
}

// installScript writes stdin to ~/.local/bin/smpmon atomically and prints
// the installed version.
const installScript = `mkdir -p "$HOME/.local/bin" && cat > "$HOME/.local/bin/smpmon.tmp" && ` +
	`chmod +x "$HOME/.local/bin/smpmon.tmp" && mv "$HOME/.local/bin/smpmon.tmp" "$HOME/.local/bin/smpmon" && ` +
	`"$HOME/.local/bin/smpmon" version`

// platformFromUname maps `uname -sm` output to GOOS and GOARCH.
func platformFromUname(out string) (goos, goarch string, err error) {
	f := strings.Fields(out)
	if len(f) < 2 {
		return "", "", fmt.Errorf("unexpected uname output %q", out)
	}
	switch strings.ToLower(f[0]) {
	case "linux":
		goos = "linux"
	case "darwin":
		goos = "darwin"
	default:
		return "", "", fmt.Errorf("unsupported OS %q", f[0])
	}
	switch f[1] {
	case "x86_64", "amd64":
		goarch = "amd64"
	case "aarch64", "arm64":
		goarch = "arm64"
	default:
		return "", "", fmt.Errorf("unsupported architecture %q", f[1])
	}
	return goos, goarch, nil
}

var releaseVersion = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// Install puts smpmon on the server in ~/.local/bin.
func Install(ctx context.Context, o InstallOptions) error {
	logf := func(format string, args ...any) {
		if o.Log != nil {
			_, _ = fmt.Fprintf(o.Log, format+"\n", args...)
		}
	}
	if o.Repo == "" {
		o.Repo = "hightemp/smpmon"
	}
	if o.HTTP == nil {
		o.HTTP = http.DefaultClient
	}
	if o.BaseURL == "" {
		o.BaseURL = "https://github.com"
	}
	if o.APIURL == "" {
		o.APIURL = "https://api.github.com"
	}

	out, err := RunSSH(ctx, o.SSH, "uname -sm", nil)
	if err != nil {
		return err
	}
	goos, goarch, err := platformFromUname(string(out))
	if err != nil {
		return fmt.Errorf("%s: %w", o.SSH.Target, err)
	}
	logf("%s is %s/%s", o.SSH.Target, goos, goarch)

	var bin []byte
	if goos == runtime.GOOS && goarch == runtime.GOARCH {
		self := o.self
		if self == nil {
			self = func() ([]byte, error) {
				exe, err := os.Executable()
				if err != nil {
					return nil, err
				}
				return os.ReadFile(exe)
			}
		}
		if bin, err = self(); err != nil {
			return err
		}
		logf("copying this smpmon (%s)", o.Version)
	} else {
		version := o.Version
		if !releaseVersion.MatchString(version) {
			if version, err = latestRelease(ctx, o); err != nil {
				return err
			}
		}
		logf("downloading smpmon %s for %s/%s", version, goos, goarch)
		if bin, err = downloadRelease(ctx, o, version, goos, goarch); err != nil {
			return err
		}
	}
	out, err = RunSSH(ctx, o.SSH, installScript, bytes.NewReader(bin))
	if err != nil {
		return fmt.Errorf("install: %w", err)
	}
	logf("installed: %s", strings.TrimSpace(string(out)))
	return nil
}

func latestRelease(ctx context.Context, o InstallOptions) (string, error) {
	body, err := get(ctx, o, o.APIURL+"/repos/"+o.Repo+"/releases/latest")
	if err != nil {
		return "", err
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", err
	}
	return strings.TrimPrefix(rel.TagName, "v"), nil
}

func get(ctx context.Context, o InstallOptions, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

// downloadRelease fetches a release archive, checks it against
// checksums.txt and returns the smpmon binary inside.
func downloadRelease(ctx context.Context, o InstallOptions, version, goos, goarch string) ([]byte, error) {
	base := fmt.Sprintf("%s/%s/releases/download/v%s/", o.BaseURL, o.Repo, version)
	name := fmt.Sprintf("smpmon_%s_%s_%s.tar.gz", version, goos, goarch)
	sums, err := get(ctx, o, base+"checksums.txt")
	if err != nil {
		return nil, err
	}
	archive, err := get(ctx, o, base+name)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(archive)
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == name {
			want = f[0]
		}
	}
	if want == "" {
		return nil, fmt.Errorf("%s is not listed in checksums.txt", name)
	}
	if hex.EncodeToString(sum[:]) != want {
		return nil, fmt.Errorf("%s: checksum mismatch", name)
	}
	return extractBinary(archive)
}

func extractBinary(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("smpmon binary not found in the archive")
		}
		if err != nil {
			return nil, err
		}
		if h.Name == "smpmon" || strings.HasSuffix(h.Name, "/smpmon") {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
}
