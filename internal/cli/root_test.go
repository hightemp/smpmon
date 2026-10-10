package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hightemp/smpmon/internal/buildinfo"
)

func TestVersionCommand(t *testing.T) {
	cmd := NewRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"version", "--log-file", filepath.Join(t.TempDir(), "smpmon.log")})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out.String(), "smpmon "+buildinfo.Version) {
		t.Fatalf("unexpected output: %q", out.String())
	}
}
