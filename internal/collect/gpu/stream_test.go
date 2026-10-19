//go:build linux || darwin

package gpu

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for " + what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestStreamConcurrent touches and closes a stream from many goroutines
// while it delivers lines; run with -race.
func TestStreamConcurrent(t *testing.T) {
	var lines atomic.Int64
	s := newStream("/bin/sh", func(string) { lines.Add(1) }, "-c", "while :; do echo x; sleep 0.01; done")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 200 {
				s.touch()
				_ = s.isRunning()
				if (i+j)%50 == 0 {
					s.close()
				}
			}
		}()
	}
	wg.Wait()
	s.touch()
	waitUntil(t, "lines", func() bool { return lines.Load() > 0 })
	s.close()
	waitUntil(t, "stop", func() bool { return !s.isRunning() })
}

// TestStreamFailed stops retrying a command that exits without output.
func TestStreamFailed(t *testing.T) {
	s := newStream("/bin/sh", func(string) {}, "-c", "exit 3")
	if !s.touch() {
		t.Fatal("first touch refused")
	}
	waitUntil(t, "exit", func() bool { return !s.isRunning() })
	if s.touch() {
		t.Fatal("failed command restarted")
	}
}

// TestStreamRestarts restarts a command that printed data and then exited.
func TestStreamRestarts(t *testing.T) {
	var lines atomic.Int64
	s := newStream("/bin/sh", func(string) { lines.Add(1) }, "-c", "echo x")
	s.touch()
	waitUntil(t, "first run", func() bool { return lines.Load() == 1 && !s.isRunning() })
	if !s.touch() {
		t.Fatal("refused after a successful run")
	}
	waitUntil(t, "second run", func() bool { return lines.Load() == 2 })
}
