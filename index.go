package pod

import (
	"sort"
	"sync"
	"time"
)

// cacheEntry is a parsed record kept in memory by the index.
type cacheEntry struct {
	Rec   *Record
	MTime time.Time
	Size  int64
}

// Index is the shared in-memory index and record cache for one store base
// directory. All Store instances opened on the same base path share a single
// Index (see Open), so queries issued from different database/sql connections
// observe each other's writes immediately.
//
// The indexer service keeps the index in sync with the filesystem; store write
// operations also update it eagerly. A table is marked "synced" only after the
// indexer has scanned it at least once (or after a store write created it), at
// which point equality lookups may be trusted to narrow query candidates.
type Index struct {
	mu     sync.RWMutex
	values map[string]map[string]map[string]struct{} // table -> "field\x00value" -> set of record ids
	all    map[string]map[string]struct{}            // table -> set of record ids
	cache  map[string]map[string]*cacheEntry         // table -> id -> entry
	synced map[string]bool                           // table -> indexer has verified it
}

// NewIndex creates an empty index.
func NewIndex() *Index {
	return &Index{
		values: map[string]map[string]map[string]struct{}{},
		all:    map[string]map[string]struct{}{},
		cache:  map[string]map[string]*cacheEntry{},
		synced: map[string]bool{},
	}
}

// valueKey builds the internal key for a field/value lookup.
func valueKey(field, value string) string { return field + "\x00" + value }

// Upsert records a parsed record (and its file metadata) into the index.
func (ix *Index) Upsert(table, id string, rec *Record, mtime time.Time, size int64) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.all[table] == nil {
		ix.all[table] = map[string]struct{}{}
		ix.cache[table] = map[string]*cacheEntry{}
	}
	ix.all[table][id] = struct{}{}
	ix.cache[table][id] = &cacheEntry{Rec: rec, MTime: mtime, Size: size}
	vm := ix.values[table]
	if vm == nil {
		vm = map[string]map[string]struct{}{}
		ix.values[table] = vm
	}
	for _, f := range rec.Fields {
		set := vm[valueKey(f.Name, f.Value)]
		if set == nil {
			set = map[string]struct{}{}
			vm[valueKey(f.Name, f.Value)] = set
		}
		set[id] = struct{}{}
	}
}

// Remove drops a record from the index.
func (ix *Index) Remove(table, id string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if all := ix.all[table]; all != nil {
		delete(all, id)
	}
	if c := ix.cache[table]; c != nil {
		delete(c, id)
	}
	if vm := ix.values[table]; vm != nil {
		for key, set := range vm {
			delete(set, id)
			if len(set) == 0 {
				delete(vm, key)
			}
		}
	}
}

// DropTable removes all index state for a table.
func (ix *Index) DropTable(table string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	delete(ix.values, table)
	delete(ix.all, table)
	delete(ix.cache, table)
	delete(ix.synced, table)
}

// All returns the indexed ids of a table. ok is false when the table is not
// known to the index at all.
func (ix *Index) All(table string) ([]string, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	ids, ok := ix.all[table]
	if !ok || len(ids) == 0 {
		return nil, ok
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, true
}

// Lookup returns the indexed record ids whose field equals value. ok is false
// when the (field, value) pair has never been indexed.
func (ix *Index) Lookup(table, field, value string) ([]string, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	vm := ix.values[table]
	if vm == nil {
		return nil, false
	}
	ids, ok := vm[valueKey(field, value)]
	if !ok || len(ids) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, true
}

// Entry returns the cached record entry for (table, id), if any.
func (ix *Index) Entry(table, id string) (*cacheEntry, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	t, ok := ix.cache[table]
	if !ok {
		return nil, false
	}
	e, ok := t[id]
	return e, ok
}

// SetSynced marks a table as fully verified by the indexer.
func (ix *Index) SetSynced(table string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.synced[table] = true
}

// Synced reports whether the index is authoritative for the table.
func (ix *Index) Synced(table string) bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.synced[table]
}

// SetSyncedUnlocked marks a table synced; caller must hold ix.mu.
func (ix *Index) SetSyncedUnlocked(table string) { ix.synced[table] = true }

// RemoveUnlocked drops a record; caller must hold ix.mu.
func (ix *Index) RemoveUnlocked(table, id string) {
	if all := ix.all[table]; all != nil {
		delete(all, id)
	}
	if c := ix.cache[table]; c != nil {
		delete(c, id)
	}
	if vm := ix.values[table]; vm != nil {
		for key, set := range vm {
			delete(set, id)
			if len(set) == 0 {
				delete(vm, key)
			}
		}
	}
}

// DropTableUnlocked removes all index state for a table; caller must hold ix.mu.
func (ix *Index) DropTableUnlocked(table string) {
	delete(ix.values, table)
	delete(ix.all, table)
	delete(ix.cache, table)
	delete(ix.synced, table)
}

// Counts returns the number of indexed tables and records.
func (ix *Index) Counts() (tables, records int) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	tables = len(ix.all)
	for _, ids := range ix.all {
		records += len(ids)
	}
	return tables, records
}
