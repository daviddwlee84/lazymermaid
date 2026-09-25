package tui

import (
	"context"
	"github.com/fsnotify/fsnotify"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Watch source directories, coalescing bursts. Buffer changes use Neovim RPC,
// not this watcher. A periodic rescan covers dropped OS watcher events.
func startWatch(ctx context.Context, path string) <-chan struct{} {
	ch := make(chan struct{}, 1)
	go func() {
		watcher, e := fsnotify.NewWatcher()
		if e != nil {
			return
		}
		defer watcher.Close()
		root := path
		if st, e := os.Stat(root); e == nil && !st.IsDir() {
			root = filepath.Dir(root)
		}
		add := func(path string) {
			_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, e error) error {
				if e != nil {
					return nil
				}
				if !d.IsDir() {
					return nil
				}
				if p != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "vendor") {
					return filepath.SkipDir
				}
				_ = watcher.Add(p)
				return nil
			})
		}
		add(root)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		var timer *time.Timer
		var tick <-chan time.Time
		send := func() {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-watcher.Events:
				if !ok {
					return
				}
				if e.Has(fsnotify.Create) {
					if st, err := os.Stat(e.Name); err == nil && st.IsDir() {
						add(e.Name)
					}
				}
				ext := strings.ToLower(filepath.Ext(e.Name))
				if ext == ".md" || ext == ".markdown" || ext == ".mmd" || ext == ".mermaid" || filepath.Base(e.Name) == ".gitignore" {
					if timer != nil {
						timer.Stop()
					}
					timer = time.NewTimer(250 * time.Millisecond)
					tick = timer.C
				}
			case <-watcher.Errors:
				send()
			case <-tick:
				tick = nil
				send()
			case <-ticker.C:
				send()
			}
		}
	}()
	return ch
}
