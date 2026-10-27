//go:build spotify

package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Soloist refuses a cache smaller than 100 MB, which is too much for the
// speaker's /data. Cached audio is kept between these sizes instead.
const (
	cacheHigh = 24 << 20
	cacheLow  = 16 << 20
)

func trimCacheLoop(ctx context.Context, dir string) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if size := cacheSize(dir); size > cacheHigh {
			trimCache(dir, cacheLow)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type cached struct {
	path string
	size int64
	mod  time.Time
}

// cachedAudio lists Soloist's cached audio chunks. Its index and settings
// files are left alone; Soloist refetches a chunk that has gone missing.
func cachedAudio(dir string) []cached {
	var files []cached
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".file") {
			return nil
		}
		if info, e := d.Info(); e == nil {
			files = append(files, cached{p, info.Size(), info.ModTime()})
		}
		return nil
	})
	return files
}

func cacheSize(dir string) int64 {
	var total int64
	for _, f := range cachedAudio(dir) {
		total += f.size
	}
	return total
}

// trimCache deletes the oldest cached audio until at most limit bytes stay.
func trimCache(dir string, limit int64) {
	files := cachedAudio(dir)
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	var total int64
	for _, f := range files {
		total += f.size
	}
	for _, f := range files {
		if total <= limit {
			return
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
}
