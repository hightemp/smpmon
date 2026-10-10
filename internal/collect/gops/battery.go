package gops

import (
	"context"
	"fmt"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

// Battery reads the battery through an OS-specific readBatteries.
type Battery struct{}

// Name implements collect.Collector.
func (b *Battery) Name() string { return "battery" }

// DefaultInterval implements collect.DefaultIntervaler.
func (b *Battery) DefaultInterval() time.Duration { return 30 * time.Second }

// Init implements collect.Collector.
func (b *Battery) Init() error {
	bats, err := readBatteries(context.Background())
	if err != nil {
		return err
	}
	if len(bats) == 0 {
		return fmt.Errorf("no battery: %w", collect.ErrUnsupported)
	}
	return nil
}

// Collect implements collect.Collector.
func (b *Battery) Collect(ctx context.Context, s *metrics.Sample) error {
	bats, err := readBatteries(ctx)
	if err != nil {
		return err
	}
	if len(bats) > 0 {
		s.Add("battery.percent", bats[0].Percent)
		s.Add("battery.power", bats[0].Power)
		s.Add("battery.time_left", bats[0].TimeLeft.Seconds())
	}
	s.SetTable(collect.TableBatteries, bats)
	return nil
}
