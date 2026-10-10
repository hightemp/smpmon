package cli

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/hightemp/smpmon/internal/collect/platform"
	"github.com/hightemp/smpmon/internal/metrics"
	"github.com/hightemp/smpmon/internal/source"
	"github.com/hightemp/smpmon/internal/ui"
)

// newFrameCommand renders a single UI frame to stdout after collecting for a
// while. It is used to produce screenshots for documentation.
func newFrameCommand(opts *rootOptions) *cobra.Command {
	var (
		w, h     int
		wait     time.Duration
		interval time.Duration
	)
	cmd := &cobra.Command{
		Use:    "frame",
		Short:  "Render one UI frame with ANSI colors to stdout",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, _, err := loadConfig(opts.configPath)
			if err != nil {
				return err
			}
			if interval > 0 {
				cfg.General.Interval = interval
			}
			src := source.NewLocal(source.LocalOptions{
				Interval:  cfg.General.Interval,
				Intervals: cfg.CollectorIntervals(),
				Store:     metrics.DefaultStoreOptions(),
			}, platform.Collectors())
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			go func() { _ = src.Run(ctx) }()

			m, err := ui.New(ui.Options{Config: cfg, Source: src, Procs: platform.ProcessController(), Dashboard: opts.dashboard})
			if err != nil {
				return err
			}
			m.Update(tea.WindowSizeMsg{Width: w, Height: h})
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), m.View().Content)
			return err
		},
	}
	f := cmd.Flags()
	f.IntVar(&w, "width", 160, "terminal width")
	f.IntVar(&h, "height", 48, "terminal height")
	f.DurationVar(&wait, "wait", 30*time.Second, "how long to collect before rendering")
	f.DurationVar(&interval, "interval", 0, "override the collection interval")
	f.StringVarP(&opts.dashboard, "dashboard", "d", "", "dashboard to render")
	return cmd
}
