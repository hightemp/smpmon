package gpu

import (
	"bufio"
	"context"
	"log/slog"
	"os/exec"
	"sync"
	"time"
)

// stream keeps a long-running command alive while its data is wanted and
// feeds every output line to onLine. It stops when touch hasn't been called
// for idleStop, so data that is not on screen costs nothing.
type stream struct {
	bin    string
	args   []string
	onLine func(string)

	mu      sync.Mutex
	lastReq time.Time
	gen     int                // number of the current run
	ctx     context.Context    // of the current run; nil before the first
	cancel  context.CancelFunc // of the current run
	running bool               // the current run hasn't ended
	failed  bool               // the command exited quickly without output: don't retry
}

// idleStop stops a stream when nobody has asked for its data for this long.
const idleStop = 10 * time.Second

func newStream(bin string, onLine func(string), args ...string) *stream {
	return &stream{bin: bin, args: args, onLine: onLine}
}

// touch records demand and starts the command if it isn't running, or if
// the current run is already stopping. It reports false once the command
// has proven unusable.
func (s *stream) touch() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastReq = time.Now()
	if s.failed {
		return false
	}
	if !s.running || s.ctx.Err() != nil {
		// A stopping run is left to finish; its end no longer affects the
		// stream state because the generation has moved on.
		ctx, cancel := context.WithCancel(context.Background())
		s.gen++
		s.ctx, s.cancel, s.running = ctx, cancel, true
		go s.run(ctx, cancel, s.gen)
	}
	return true
}

func (s *stream) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *stream) run(ctx context.Context, cancel context.CancelFunc, gen int) {
	started, lines := time.Now(), 0
	defer func() {
		stopped := ctx.Err() != nil // by us: idle, close or a newer run
		cancel()
		s.mu.Lock()
		defer s.mu.Unlock()
		if gen != s.gen {
			return // a newer run took over
		}
		// A command that dies within seconds without printing anything is
		// unsupported here (e.g. pmon on Windows WDDM): stop retrying.
		if !stopped && lines == 0 && time.Since(started) < 5*time.Second {
			s.failed = true
		}
		s.running = false
	}()
	cmd := exec.CommandContext(ctx, s.bin, s.args...)
	dieWithParent(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		slog.Debug("nvidia-smi pipe", "args", s.args, "err", err)
		return
	}
	if err := cmd.Start(); err != nil {
		slog.Debug("nvidia-smi start", "args", s.args, "err", err)
		return
	}
	defer func() { _ = cmd.Wait() }()

	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.mu.Lock()
				idle := time.Since(s.lastReq) > idleStop
				s.mu.Unlock()
				if idle {
					cancel()
				}
			}
		}
	}()

	sc := bufio.NewScanner(out)
	for sc.Scan() {
		lines++
		s.onLine(sc.Text())
	}
}

func (s *stream) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}
