package cli

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/collect/platform"
	"github.com/hightemp/smpmon/internal/metrics"
	"github.com/hightemp/smpmon/internal/source"
	"github.com/hightemp/smpmon/internal/ui"
)

func runTUI(ctx context.Context, opts *rootOptions) error {
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
	src := source.NewLocal(source.LocalOptions{
		Interval:  cfg.General.Interval,
		Intervals: cfg.CollectorIntervals(),
		Store:     metrics.DefaultStoreOptions(),
	}, collectors)

	ctx, cancel := context.WithCancel(ctx)
	defer stopSource(cancel, startSource(ctx, src))

	// Only watch a config file that exists; a missing default is fine.
	watch := ""
	if _, err := os.Stat(path); err == nil {
		watch = path
	}
	return ui.Run(ctx, ui.Options{
		Config:     cfg,
		ConfigPath: watch,
		SavePath:   path,
		Source:     src,
		Procs:      platform.ProcessController(),
		Dashboard:  opts.dashboard,
	})
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
