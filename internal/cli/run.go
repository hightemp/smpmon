package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/hightemp/smpmon/internal/alert"
	"github.com/hightemp/smpmon/internal/buildinfo"
	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/collect/platform"
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/history"
	"github.com/hightemp/smpmon/internal/metrics"
	"github.com/hightemp/smpmon/internal/remote"
	"github.com/hightemp/smpmon/internal/source"
	"github.com/hightemp/smpmon/internal/ui"
)

// sshBinary is the ssh client; SMPMON_SSH overrides it (e.g. a wrapper
// script or a specific build).
var sshBinary = cmpOr(os.Getenv("SMPMON_SSH"), "ssh")

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// remoteHost is a host to connect to.
type remoteHost struct {
	name string
	cfg  config.Host
}

// resolveTargets maps `connect` arguments to hosts: names from the config,
// or anything else as an SSH target.
func resolveTargets(cfg *config.Config, args []string) []remoteHost {
	out := make([]remoteHost, 0, len(args))
	for _, a := range args {
		if h, ok := cfg.Hosts[a]; ok {
			out = append(out, remoteHost{a, h})
			continue
		}
		out = append(out, remoteHost{a, config.Host{SSH: a}})
	}
	return out
}

func newRemote(cfg *config.Config, h remoteHost) (*remote.Client, error) {
	var dial remote.Dialer
	token := ""
	switch {
	case h.cfg.SSH != "":
		dial = remote.SSHDialer(remote.SSHOptions{Target: h.cfg.SSH, Command: h.cfg.Command, Binary: sshBinary})
	case h.cfg.URL != "":
		var err error
		if dial, err = remote.TLSDialer(remote.TLSOptions{URL: h.cfg.URL, Fingerprint: h.cfg.Fingerprint}); err != nil {
			return nil, fmt.Errorf("host %s: %w", h.name, err)
		}
		if token, err = remote.ReadToken(expandHome(h.cfg.TokenFile)); err != nil {
			return nil, fmt.Errorf("host %s: %w", h.name, err)
		}
	default:
		return nil, fmt.Errorf("host %s: no ssh or url", h.name)
	}
	return remote.NewClient(remote.ClientOptions{
		Name: h.name, Dial: dial, Token: token, Interval: cfg.General.Interval, Store: metrics.DefaultStoreOptions(),
	}), nil
}

func expandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return home + "/" + rest
		}
	}
	return p
}

// runTUI shows this machine and the given remote hosts. With no targets,
// all hosts from the config are added. The first target is shown first.
func runTUI(ctx context.Context, opts *rootOptions, targets []remoteHost) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg, path, err := loadConfig(opts.configPath)
	if err != nil {
		return err
	}
	var collectors []collect.Collector
	for _, c := range platform.Collectors() {
		if cfg.CollectorEnabled(c.Name()) {
			collectors = append(collectors, c)
		}
	}
	if len(collectors) == 0 {
		return errors.New("no collectors available on this platform yet")
	}
	local := ui.HostSource{
		Name: "local",
		Source: source.NewLocal(source.LocalOptions{
			Interval:  cfg.General.Interval,
			Intervals: cfg.CollectorIntervals(),
			Store:     metrics.DefaultStoreOptions(),
		}, collectors),
		Procs: platform.ProcessController(),
	}

	remotes := targets
	if remotes == nil {
		for _, name := range cfg.HostNames() {
			remotes = append(remotes, remoteHost{name, cfg.Hosts[name]})
		}
	}
	var hosts []ui.HostSource
	for _, r := range remotes {
		c, err := newRemote(cfg, r)
		if err != nil {
			return err
		}
		hosts = append(hosts, ui.HostSource{Name: r.name, Source: c, Procs: c.ProcessController()})
	}
	current := "local"
	if targets != nil && len(hosts) > 0 {
		current = hosts[0].Name
	}
	hosts = append([]ui.HostSource{local}, hosts...)

	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	if cfg.History.Enabled {
		if err := setupHistory(ctx, cfg, hosts, &wg); err != nil {
			cancel()
			return err
		}
	}
	engine, err := setupAlerts(ctx, cfg, hosts, &wg)
	if err != nil {
		cancel()
		return err
	}
	for _, h := range hosts {
		done := startSource(ctx, h.Source)
		wg.Add(1)
		go func() { defer wg.Done(); <-done }()
	}
	defer func() {
		cancel()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}()

	// Only watch a config file that exists; a missing default is fine.
	watch := ""
	if _, err := os.Stat(path); err == nil {
		watch = path
	}
	return ui.Run(ctx, ui.Options{
		Config:     cfg,
		ConfigPath: watch,
		SavePath:   path,
		Hosts:      hosts,
		Host:       current,
		Dashboard:  opts.dashboard,
		Alerts:     engine,
	})
}

func newConnectCommand(opts *rootOptions) *cobra.Command {
	var install bool
	cmd := &cobra.Command{
		Use:   "connect HOST...",
		Short: "Monitor remote machines over SSH",
		Long: `Monitor remote machines. HOST is a name from the hosts: section of the
config or anything ssh accepts (user@server, an alias from ~/.ssh/config).

SSH hosts need key-based login (an ssh-agent) and smpmon on the server;
--install copies or downloads the right binary to ~/.local/bin there.
Press H to switch hosts.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := loadConfig(opts.configPath)
			if err != nil {
				return err
			}
			targets := resolveTargets(cfg, args)
			if install {
				for _, t := range targets {
					if t.cfg.SSH == "" {
						return fmt.Errorf("--install works over SSH only; %s is a network agent", t.name)
					}
					if err := remote.Install(cmd.Context(), remote.InstallOptions{
						SSH:     remote.SSHOptions{Target: t.cfg.SSH, Binary: sshBinary},
						Version: buildinfo.Version,
						Log:     cmd.ErrOrStderr(),
					}); err != nil {
						return err
					}
				}
			}
			return runTUI(cmd.Context(), opts, targets)
		},
	}
	cmd.Flags().BoolVar(&install, "install", false, "install smpmon on the hosts first")
	return cmd
}

// startSource runs src in the background and returns a channel closed when
// it has stopped.
func startSource(ctx context.Context, src source.Source) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = src.Run(ctx)
	}()
	return done
}

// stopSource cancels the source and waits briefly for collectors to clean
// up (e.g. stop nvidia-smi).
func stopSource(cancel context.CancelFunc, done <-chan struct{}) {
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

// allCollectors is what a "record everything" history keeps running.
var allCollectors = []string{"cpu", "load", "mem", "disks", "net", "sensors", "gpu", "battery", "docker", "procs"}

// setupHistory starts a recorder per host, each in its own directory.
func setupHistory(ctx context.Context, cfg *config.Config, hosts []ui.HostSource, wg *sync.WaitGroup) error {
	hc := cfg.History
	base := expandHome(hc.Path)
	if base == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return err
		}
		base = filepath.Join(dir, "smpmon", "history")
	}
	patterns := hc.Metrics
	if len(patterns) == 0 {
		patterns = history.DefaultMetrics
	}
	sels := make([]metrics.Selector, 0, len(patterns))
	for _, p := range patterns {
		sel, err := metrics.ParseSelector(p)
		if err != nil {
			return fmt.Errorf("history.metrics: %w", err)
		}
		sels = append(sels, sel)
	}
	keep := collect.CollectorsFor(patterns)
	if keep == nil {
		keep = allCollectors
	}
	if hc.Processes && !slices.Contains(keep, "procs") {
		keep = append(slices.Clone(keep), "procs")
	}
	retention := map[history.Tier]time.Duration{}
	for tier, d := range history.DefaultRetention {
		retention[tier] = d
	}
	for tier, d := range map[history.Tier]time.Duration{history.Raw: hc.Retention.Raw, history.Ten: hc.Retention.Ten, history.Minute: hc.Retention.Minute} {
		if d > 0 {
			retention[tier] = d
		}
	}
	retention[history.Tables] = max(retention[history.Ten], retention[history.Tables])
	for i := range hosts {
		h := &hosts[i]
		dir := filepath.Join(base, safeName(h.Name))
		rec := history.NewRecorder(h.Source.Store(), history.Options{
			Dir: dir, Metrics: sels, Processes: hc.Processes, Retention: retention,
			MaxSize: int64(hc.MaxSizeMB) << 20,
		})
		h.HistoryDir, h.Recorder, h.Keep = dir, rec, keep
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := rec.Run(ctx); err != nil {
				slog.Warn("history", "host", h.Name, "err", err)
			}
		}()
	}
	return nil
}

// safeName makes a host name usable as a directory name on every OS.
func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_', r == '@':
			return r
		}
		return '_'
	}, s)
}

// setupAlerts starts the alert engine over all hosts, or returns nil without
// rules. Collectors that rules depend on keep running.
func setupAlerts(ctx context.Context, cfg *config.Config, hosts []ui.HostSource, wg *sync.WaitGroup) (*alert.Engine, error) {
	if len(cfg.Alerts) == 0 {
		return nil, nil
	}
	rules, err := alert.Compile(cfg.Alerts)
	if err != nil {
		return nil, err
	}
	engine := alert.NewEngine(rules, &alert.Dispatcher{})
	for i := range hosts {
		h := &hosts[i]
		engine.AddHost(h.Name, h.Source.Store())
		for _, r := range rules {
			if len(r.Hosts) > 0 && !slices.Contains(r.Hosts, h.Name) {
				continue
			}
			need := collect.CollectorsFor([]string{r.Selector.String()})
			if need == nil {
				need = allCollectors
			}
			for _, c := range need {
				if !slices.Contains(h.Keep, c) {
					h.Keep = append(h.Keep, c)
				}
			}
		}
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		engine.Run(ctx)
	}()
	return engine, nil
}
