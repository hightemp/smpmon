package config

import (
	"context"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watchDebounce coalesces the burst of events editors produce on save.
const watchDebounce = 200 * time.Millisecond

// Watch calls onChange whenever the file at path is written, created or
// replaced, until ctx is cancelled. The directory is watched rather than the
// file because many editors save by renaming a temporary file over it.
func Watch(ctx context.Context, path string, onChange func()) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		_ = w.Close()
		return err
	}
	if err := w.Add(filepath.Dir(abs)); err != nil {
		_ = w.Close()
		return err
	}
	go func() {
		defer func() { _ = w.Close() }()
		var timer *time.Timer
		var fire <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if filepath.Clean(ev.Name) != abs || !ev.Has(fsnotify.Write|fsnotify.Create|fsnotify.Rename) {
					continue
				}
				if timer == nil {
					timer = time.NewTimer(watchDebounce)
				} else {
					timer.Reset(watchDebounce)
				}
				fire = timer.C
			case <-fire:
				fire = nil
				onChange()
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
			}
		}
	}()
	return nil
}
