package pod

import (
	"context"
	"log"
	"sync"
	"time"
)

// Normalizer is a background service that upgrades older XML record files to
// the current table schema. It compares each record against the table's
// column definitions and backfills any field that is missing but has a defined
// default value (for example a newly added column). Existing values are never
// overwritten.
type Normalizer struct {
	store     *Store
	mu        sync.Mutex
	lastRun   time.Time
	lastCount int
}

// NewNormalizer creates a normalizer for the store.
func NewNormalizer(s *Store) *Normalizer {
	return &Normalizer{store: s}
}

// Status returns when the normalizer last ran and how many files it upgraded.
func (n *Normalizer) Status() (time.Time, int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lastRun, n.lastCount
}

// Run drives RunOnce at the given interval until ctx is cancelled.
func (n *Normalizer) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if count, err := n.RunOnce(); err != nil {
				log.Printf("pod normalizer: failed: %v", err)
			} else if count > 0 {
				log.Printf("pod normalizer: upgraded %d records", count)
			}
		}
	}
}

// RunOnce scans every table and upgrades records that are missing fields
// covered by schema defaults. It returns the number of files changed.
func (n *Normalizer) RunOnce() (int, error) {
	tables, err := n.store.ListTables()
	if err != nil {
		return 0, err
	}
	total := 0
	for _, table := range tables {
		sc, err := n.store.Schema(table)
		if err != nil {
			continue
		}
		if sc == nil || len(sc.Columns) == 0 {
			continue // no schema yet; nothing to normalize against
		}
		ids, err := n.store.IDs(table)
		if err != nil {
			continue
		}
		for _, id := range ids {
			changed, err := n.store.NormalizeRecord(table, id)
			if err != nil {
				continue
			}
			if changed {
				total++
			}
		}
	}
	n.mu.Lock()
	n.lastRun = time.Now()
	n.lastCount = total
	n.mu.Unlock()
	return total, nil
}
