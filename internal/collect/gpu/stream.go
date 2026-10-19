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
	running bool
	cancel  context.CancelFunc
	started time.Time
	lines   int  // lines read from the current run
	failed  bool // the command exited quickly without output: don't retry
}

// idleStop stops a stream when nobody has asked for its data for this long.
const idleStop = 10 * time.Second

func newStream(bin string, onLine func(string), args ...string) *stream {
	return &stream{bin: bin, args: args, onLine: onLine}
}

// touch records demand and starts the command if it isn't running. It
// reports false once the command has proven unusable.
func (s *stream) touch() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastReq = time.Now()
	if s.failed {
		return false
	}
	if !s.running {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel, s.running, s.started, s.lines = cancel, true, time.Now(), 0
		go s.run(ctx)
	}
	return true
}

func (s *stream) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *stream) run(ctx context.Context) {
	defer func() {
		s.mu.Lock()
		// A command that dies within seconds without printing anything is
		// unsupported here (e.g. pmon on Windows WDDM): stop retrying.
		if ctx.Err() == nil && s.lines == 0 && time.Since(s.started) < 5*time.Second {
			s.failed = true
		}
		s.running = false
		s.cancel()
		s.mu.Unlock()
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
				if time.Since(s.lastReq) > idleStop {
					s.cancel()
				}
				s.mu.Unlock()
			}
		}
	}()

	sc := bufio.NewScanner(out)
	for sc.Scan() {
		s.mu.Lock()
		s.lines++
		s.mu.Unlock()
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
