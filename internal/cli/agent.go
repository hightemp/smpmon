package cli

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/hightemp/smpmon/internal/buildinfo"
	"github.com/hightemp/smpmon/internal/collect/platform"
	"github.com/hightemp/smpmon/internal/remote"
)

// stdio joins stdin and stdout into one stream for the agent.
type stdio struct{}

func (stdio) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdio) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (stdio) Close() error                { return os.Stdin.Close() }

func newAgentCommand() *cobra.Command {
	var (
		useStdio                bool
		listen                  string
		certFile, keyFile       string
		tokenFile, controlToken string
		insecure                bool
	)
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Serve this machine's metrics to a remote smpmon",
		Long: `Serve this machine's metrics to smpmon running elsewhere.

--stdio is used by "smpmon connect" over SSH and needs no setup.
--listen runs a network agent with TLS and token authentication for
permanent installations; see "smpmon agent gen-cert" and "gen-token".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			opts := remote.AgentOptions{
				Collectors: platform.Collectors,
				Procs:      platform.ProcessController(),
				Version:    buildinfo.Version,
			}
			switch {
			case useStdio && listen != "":
				return errors.New("use either --stdio or --listen")
			case useStdio:
				// The SSH user can run kill/renice in a shell anyway.
				opts.Control = true
				return remote.Serve(ctx, stdio{}, opts)
			case listen != "":
				var err error
				if tokenFile != "" {
					if opts.ReadToken, err = remote.ReadToken(tokenFile); err != nil {
						return err
					}
				}
				if controlToken != "" {
					if opts.ControlToken, err = remote.ReadToken(controlToken); err != nil {
						return err
					}
				}
				if !insecure && (certFile == "" || keyFile == "") {
					return errors.New("--cert and --key are required (or --insecure on a trusted network)")
				}
				if insecure {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warning: --insecure: traffic and tokens are sent unencrypted")
				}
				return remote.Listen(ctx, remote.ListenOptions{
					Addr: listen, CertFile: certFile, KeyFile: keyFile, Insecure: insecure, Agent: opts,
				})
			}
			return errors.New("use --stdio (for smpmon connect) or --listen ADDR")
		},
	}
	f := cmd.Flags()
	f.BoolVar(&useStdio, "stdio", false, "serve one session on stdin/stdout (used over SSH)")
	f.StringVar(&listen, "listen", "", "address for the network agent, e.g. :9200")
	f.StringVar(&certFile, "cert", "", "TLS certificate (PEM)")
	f.StringVar(&keyFile, "key", "", "TLS key (PEM)")
	f.StringVar(&tokenFile, "token-file", "", "file with the read-only token")
	f.StringVar(&controlToken, "control-token-file", "", "file with the token that also allows kill/renice")
	f.BoolVar(&insecure, "insecure", false, "serve without TLS (trusted networks only)")

	var hosts []string
	var outDir string
	genCert := &cobra.Command{
		Use:   "gen-cert",
		Short: "Create a self-signed certificate for --listen",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(hosts) == 0 {
				h, _ := os.Hostname()
				hosts = []string{h}
			}
			if err := os.MkdirAll(outDir, 0o755); err != nil {
				return err
			}
			cert, key := filepath.Join(outDir, "agent.crt"), filepath.Join(outDir, "agent.key")
			fp, err := remote.GenerateCert(cert, key, hosts)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "wrote %s and %s\nfingerprint: %s\n"+
				"clients pin it with:  fingerprint: %q\n", cert, key, fp, fp)
			return err
		},
	}
	genCert.Flags().StringSliceVar(&hosts, "host", nil, "DNS names or IPs for the certificate (default: hostname)")
	genCert.Flags().StringVar(&outDir, "out", ".", "output directory")

	genToken := &cobra.Command{
		Use:   "gen-token",
		Short: "Print a random token for --token-file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := remote.GenerateToken()
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), t)
			return err
		},
	}
	cmd.AddCommand(genCert, genToken)
	return cmd
}
