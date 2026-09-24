package pod

import (
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Indexer keeps the shared Index in sync with the filesystem. It is a
// background service: Rebuild performs a full scan; Sync performs a fast
// incremental pass comparing file metadata with the cache; Run drives Sync
// periodically from a goroutine.
type Indexer struct {
	store *Store
	mu    sync.Mutex
	last  time.Time
	count int
}

// NewIndexer creates an indexer for the store.
func NewIndexer(s *Store) *Indexer {
	return &Indexer{store: s}
}

// Status returns the time of the last sync and the number of files it touched.
func (x *Indexer) Status() (time.Time, int) {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.last, x.count
}

// Run drives Sync at the given interval until ctx is cancelled.
func (x *Indexer) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := x.Sync(); err != nil {
				log.Printf("pod indexer: sync failed: %v", err)
				continue
			}
			if _, n := x.Status(); n > 0 {
				log.Printf("pod indexer: synced, touched %d files", n)
			}
		}
	}
}

// Rebuild clears the index and scans the whole store from disk.
func (x *Indexer) Rebuild() error {
	base := x.store.base
	ix := x.store.ix

	// Reset the index for a clean rebuild by capturing the tables we forget.
	// Simplest correct approach: drop everything and re-scan.
	ix.mu.Lock()
	ix.values = map[string]map[string]map[string]struct{}{}
	ix.all = map[string]map[string]struct{}{}
	ix.cache = map[string]map[string]*cacheEntry{}
	ix.synced = map[string]bool{}
	ix.mu.Unlock()

	touched := 0
	var walkErr error
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries; keep walking
		}
		if path == base || !d.IsDir() {
			return nil
		}
		if !isTableDir(path) {
			return nil
		}
		rel, rerr := filepath.Rel(base, path)
		if rerr != nil {
			return nil
		}
		table := filepath.ToSlash(rel)
		n, serr := x.scanTable(table)
		if serr != nil && walkErr == nil {
			walkErr = serr
		}
		touched += n
		return nil
	})
	if err != nil && walkErr == nil {
		walkErr = err
	}

	x.mu.Lock()
	x.last = time.Now()
	x.count = touched
	x.mu.Unlock()
	return walkErr
}

// Sync performs an incremental pass: it scans every table directory and
// updates the index for changed, new, or removed files.
func (x *Indexer) Sync() error {
	base := x.store.base
	touched := 0
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == base || !d.IsDir() {
			return nil
		}
		if !isTableDir(path) {
			return nil
		}
		rel, rerr := filepath.Rel(base, path)
		if rerr != nil {
			return nil
		}
		table := filepath.ToSlash(rel)
		n, serr := x.syncTable(table)
		if serr != nil {
			return serr
		}
		touched += n
		return nil
	})
	if err != nil {
		return err
	}

	// Drop index state for tables that no longer exist on disk.
	x.store.ix.mu.Lock()
	for table := range x.store.ix.all {
		dir, derr := x.store.TablePath(table)
		if derr != nil {
			continue
		}
		if _, serr := os.Stat(dir); os.IsNotExist(serr) {
			x.store.ix.DropTableUnlocked(table)
		}
	}
	x.store.ix.mu.Unlock()

	x.mu.Lock()
	x.last = time.Now()
	x.count = touched
	x.mu.Unlock()
	return nil
}

// scanTable fully reindexes one table directory, returning the number of
// record files read.
func (x *Indexer) scanTable(table string) (int, error) {
	dir, err := x.store.TablePath(table)
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, e := range entries {
		if e.IsDir() || !isRecordFile(e.Name()) {
			continue
		}
		if _, err := x.reindexTableFile(table, e); err != nil {
			continue
		}
		count++
	}
	return count, nil
}

// syncTable incrementally syncs one table directory.
func (x *Indexer) syncTable(table string) (int, error) {
	dir, err := x.store.TablePath(table)
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	count := 0
	seen := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if e.IsDir() || !isRecordFile(e.Name()) {
			continue
		}
		seen[e.Name()] = struct{}{}
		id := strings.TrimSuffix(e.Name(), ".xml")
		if x.upToDate(table, id, e) {
			continue
		}
		if _, err := x.reindexTableFile(table, e); err != nil {
			continue
		}
		count++
	}
	// Remove ids that are no longer on disk.
	x.store.ix.mu.Lock()
	if cached := x.store.ix.cache[table]; cached != nil {
		for id := range cached {
			if _, ok := seen[recordFileName(id)]; !ok {
				x.store.ix.RemoveUnlocked(table, id)
				count++
			}
		}
	}
	x.store.ix.SetSyncedUnlocked(table)
	x.store.ix.mu.Unlock()
	return count, nil
}

// upToDate reports whether the cached entry already reflects the file metadata.
func (x *Indexer) upToDate(table, id string, e fs.DirEntry) bool {
	info, err := e.Info()
	if err != nil {
		return false
	}
	if c, ok := x.store.ix.Entry(table, id); ok {
		return c.MTime.Equal(info.ModTime()) && c.Size == info.Size()
	}
	return false
}

// reindexTableFile re-reads one record file into the index.
func (x *Indexer) reindexTableFile(table string, e fs.DirEntry) (*cacheEntry, error) {
	dir, err := x.store.TablePath(table)
	if err != nil {
		return nil, err
	}
	info, err := e.Info()
	if err != nil {
		return nil, err
	}
	rec, err := loadRecordFile(filepath.Join(dir, e.Name()))
	if err != nil {
		return nil, err
	}
	id := strings.TrimSuffix(e.Name(), ".xml")
	if rec.ID != id {
		rec.ID = id // directory listings are authoritative for the id
	}
	x.store.ix.Upsert(table, id, rec, info.ModTime(), info.Size())
	return nil, nil
}

// isTableDir reports whether a directory holds database content (a schema
// file and/or at least one record file).
func isTableDir(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == schemaFileName {
			return true
		}
		if isRecordFile(name) {
			return true
		}
	}
	return false
}

// isRecordFile reports whether a file name looks like a record XML file.
func isRecordFile(name string) bool {
	return !strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".xml")
}
