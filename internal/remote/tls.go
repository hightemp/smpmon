package remote

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

// maxSessions limits concurrent TLS clients.
const maxSessions = 16

// ListenOptions configures the network agent.
type ListenOptions struct {
	Addr     string
	CertFile string
	KeyFile  string
	Insecure bool // plain TCP; only for testing on trusted networks
	Agent    AgentOptions
}

// Listen serves agent sessions over TLS until ctx is cancelled.
func Listen(ctx context.Context, o ListenOptions) error {
	if o.Agent.ReadToken == "" && o.Agent.ControlToken == "" {
		return errors.New("a token is required (--token-file or --control-token-file)")
	}
	var l net.Listener
	var err error
	if o.Insecure {
		l, err = net.Listen("tcp", o.Addr)
	} else {
		var cert tls.Certificate
		cert, err = tls.LoadX509KeyPair(o.CertFile, o.KeyFile)
		if err != nil {
			return fmt.Errorf("load certificate: %w", err)
		}
		l, err = tls.Listen("tcp", o.Addr, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
	}
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	slog.Info("agent listening", "addr", l.Addr().String(), "tls", !o.Insecure)
	sem := make(chan struct{}, maxSessions)
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		select {
		case sem <- struct{}{}:
		default:
			slog.Warn("too many sessions, rejecting", "remote", c.RemoteAddr().String())
			_ = c.Close()
			continue
		}
		go func() {
			defer func() { <-sem }()
			if err := Serve(ctx, c, o.Agent); err != nil {
				slog.Info("session ended", "remote", c.RemoteAddr().String(), "err", err)
			}
		}()
	}
}

// TLSOptions configures a client connection to a network agent.
type TLSOptions struct {
	URL         string // tls://host:port (or tcp:// for --insecure agents)
	Fingerprint string // "sha256:<hex>" pin for self-signed certificates
}

// TLSDialer connects to a network agent.
func TLSDialer(o TLSOptions) (Dialer, error) {
	u, err := url.Parse(o.URL)
	if err != nil {
		return nil, err
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "9200")
	}
	switch u.Scheme {
	case "tcp":
		return func(ctx context.Context) (io.ReadWriteCloser, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", u.Host)
		}, nil
	case "tls":
	default:
		return nil, fmt.Errorf("unsupported URL scheme %q (want tls:// or tcp://)", u.Scheme)
	}
	cfg := &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS13}
	if o.Fingerprint != "" {
		want, err := parseFingerprint(o.Fingerprint)
		if err != nil {
			return nil, err
		}
		// A pinned certificate replaces CA verification.
		cfg.InsecureSkipVerify = true
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("no server certificate")
			}
			got := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if string(got[:]) != string(want) {
				return fmt.Errorf("certificate fingerprint mismatch: got %s", Fingerprint(cs.PeerCertificates[0].Raw))
			}
			return nil
		}
	}
	return func(ctx context.Context) (io.ReadWriteCloser, error) {
		d := tls.Dialer{Config: cfg}
		return d.DialContext(ctx, "tcp", u.Host)
	}, nil
}

// Fingerprint formats a certificate's SHA-256 fingerprint.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func parseFingerprint(s string) ([]byte, error) {
	h, ok := strings.CutPrefix(strings.ToLower(s), "sha256:")
	if !ok {
		return nil, fmt.Errorf("fingerprint %q: want sha256:<hex>", s)
	}
	b, err := hex.DecodeString(strings.ReplaceAll(h, ":", ""))
	if err != nil || len(b) != sha256.Size {
		return nil, fmt.Errorf("fingerprint %q: want 64 hex digits", s)
	}
	return b, nil
}

// GenerateCert writes a self-signed ECDSA certificate and key for hosts and
// returns its fingerprint.
func GenerateCert(certFile, keyFile string, hosts []string) (string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "smpmon agent"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return "", err
	}
	return Fingerprint(der), nil
}

// GenerateToken returns a random 256-bit token as hex.
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ReadToken reads a token file, trimming whitespace.
func ReadToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	t := strings.TrimSpace(string(b))
	if len(t) < 16 {
		return "", fmt.Errorf("%s: token is too short (use `smpmon agent gen-token`)", path)
	}
	return t, nil
}
