package remote

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/source"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func TestTLSAgent(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "a.crt"), filepath.Join(dir, "a.key")
	fp, err := GenerateCert(cert, key, []string{"127.0.0.1", "localhost"})
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(key); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v", st.Mode())
	}
	token, _ := GenerateToken()
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Listen(ctx, ListenOptions{Addr: addr, CertFile: cert, KeyFile: key, Agent: AgentOptions{
			Collectors: func() []collect.Collector { return []collect.Collector{&fakeCollector{name: "cpu"}} },
			Procs:      &fakeProcs{}, ReadToken: token,
		}})
	}()
	time.Sleep(200 * time.Millisecond)

	connect := func(fingerprint, tok string) *Client {
		dial, err := TLSDialer(TLSOptions{URL: "tls://" + addr, Fingerprint: fingerprint})
		if err != nil {
			t.Fatal(err)
		}
		c := NewClient(ClientOptions{Name: "edge", Dial: dial, Token: tok, Interval: 100 * time.Millisecond})
		c.SetDemand(nil)
		go func() { _ = c.Run(ctx) }()
		return c
	}
	good := connect(fp, token)
	waitFor(t, "tls data", func() bool { return good.Store().Has("cpu.n") })
	if good.Conn().State != source.Online {
		t.Fatal("not online")
	}
	if err := good.ProcessController().Signal(1, 15); err == nil {
		t.Fatal("read token allowed an action")
	}

	wrongPin := connect("sha256:"+strings.Repeat("ab", 32), token)
	waitFor(t, "pin mismatch", func() bool {
		err := wrongPin.Conn().Err
		return err != nil && strings.Contains(err.Error(), "fingerprint mismatch")
	})
	noPin := connect("", token) // self-signed without a pin fails CA verification
	waitFor(t, "untrusted cert", func() bool { return noPin.Conn().Err != nil })
	badToken := connect(fp, "nope-nope-nope-nope")
	waitFor(t, "bad token", func() bool {
		err := badToken.Conn().Err
		return err != nil && strings.Contains(err.Error(), "authentication failed")
	})

	// Without any token the agent refuses to start.
	if err := Listen(ctx, ListenOptions{Addr: freeAddr(t), Insecure: true}); err == nil {
		t.Fatal("listen without token")
	}
	for _, bad := range []string{"sha256:zz", "md5:00", "sha256:abcd"} {
		if _, err := TLSDialer(TLSOptions{URL: "tls://x:1", Fingerprint: bad}); err == nil {
			t.Errorf("accepted fingerprint %q", bad)
		}
	}
	if _, err := TLSDialer(TLSOptions{URL: "http://x"}); err == nil {
		t.Error("accepted http://")
	}
}

func TestInsecureAgent(t *testing.T) {
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Listen(ctx, ListenOptions{Addr: addr, Insecure: true, Agent: AgentOptions{
			Collectors: func() []collect.Collector { return []collect.Collector{&fakeCollector{name: "cpu"}} },
			Procs:      &fakeProcs{}, ControlToken: "control-token-123456",
		}})
	}()
	time.Sleep(200 * time.Millisecond)
	dial, err := TLSDialer(TLSOptions{URL: "tcp://" + addr})
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(ClientOptions{Name: "lab", Dial: dial, Token: "control-token-123456", Interval: 100 * time.Millisecond})
	c.SetDemand(nil)
	go func() { _ = c.Run(ctx) }()
	waitFor(t, "data", func() bool { return c.Store().Has("cpu.n") })
	if err := c.ProcessController().Signal(1, 15); err != nil {
		t.Fatalf("control token refused: %v", err)
	}
}
