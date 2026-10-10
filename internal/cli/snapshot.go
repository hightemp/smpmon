package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/collect/platform"
	"github.com/hightemp/smpmon/internal/metrics"
)

func newSnapshotCommand() *cobra.Command {
	var (
		asJSON   bool
		interval time.Duration
		selector string
	)
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Collect metrics once and print them",
		Long: "Collect all metrics twice, --interval apart (rates need two samples), " +
			"and print the latest values.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sel, err := metrics.ParseSelector(selector)
			if err != nil {
				return err
			}
			snap, err := takeSnapshot(cmd.Context(), platform.Collectors(), interval, sel)
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(snap)
			}
			return printSnapshot(cmd.OutOrStdout(), snap)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&asJSON, "json", false, "print JSON (metrics and tables)")
	f.DurationVarP(&interval, "interval", "i", time.Second, "time between the two samples")
	f.StringVarP(&selector, "select", "s", "**", "metric selector, e.g. 'cpu.**'")
	return cmd
}

type snapshot struct {
	Time    time.Time          `json:"time"`
	Metrics map[string]float64 `json:"metrics"`
	Tables  map[string]any     `json:"tables,omitempty"`
	Errors  map[string]string  `json:"errors,omitempty"`
}

func takeSnapshot(ctx context.Context, cs []collect.Collector, interval time.Duration, sel metrics.Selector) (*snapshot, error) {
	if len(cs) == 0 {
		return nil, errors.New("no collectors available on this platform yet")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	snap := &snapshot{Metrics: map[string]float64{}, Tables: map[string]any{}, Errors: map[string]string{}}
	var active []collect.Collector
	for _, c := range cs {
		if err := c.Init(); err != nil {
			snap.Errors[c.Name()] = err.Error()
			continue
		}
		active = append(active, c)
	}
	var last []metrics.Sample
	for round := range 2 {
		if round == 1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(interval):
			}
		}
		last = last[:0]
		for _, c := range active {
			s := metrics.Sample{Time: time.Now()}
			if err := c.Collect(ctx, &s); err != nil {
				snap.Errors[c.Name()] = err.Error()
				continue
			}
			last = append(last, s)
		}
	}
	snap.Time = time.Now()
	for _, s := range last {
		for _, p := range s.Points {
			if sel.Match(p.Key) {
				snap.Metrics[string(p.Key)] = p.Value
			}
		}
		for name, t := range s.Tables {
			snap.Tables[name] = t
		}
	}
	return snap, nil
}

func printSnapshot(w io.Writer, snap *snapshot) error {
	keys := make([]metrics.Key, 0, len(snap.Metrics))
	for k := range snap.Metrics {
		keys = append(keys, metrics.Key(k))
	}
	slices.SortFunc(keys, metrics.CompareKeys)
	width := 0
	for _, k := range keys {
		width = max(width, len(k))
	}
	for _, k := range keys {
		v := snap.Metrics[string(k)]
		if _, err := fmt.Fprintf(w, "%-*s  %s\n", width, k, metrics.Format(v, metrics.DefaultRegistry.UnitOf(k))); err != nil {
			return err
		}
	}
	for name, msg := range snap.Errors {
		if _, err := fmt.Fprintf(w, "error: %s: %s\n", name, msg); err != nil {
			return err
		}
	}
	return nil
}
