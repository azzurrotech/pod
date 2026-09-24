package pod

import (
	"crypto/rand"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrNotFound is returned when a record (or table) does not exist.
var ErrNotFound = errors.New("pod: record not found")

// QueryOptions controls ordering and pagination of a query.
type QueryOptions struct {
	OrderBy string
	Desc    bool
	Limit   int
	Offset  int
}

// TableInfo summarizes a table for listing endpoints.
type TableInfo struct {
	Name   string  `json:"name"`
	Count  int     `json:"count"`
	Schema *Schema `json:"schema,omitempty"`
}

// Store is the filesystem XML database. Records are stored one XML file per
// row under <base>/<table-path>/. Table paths may contain slashes to create
// siloed directories; the destination path of an HTTP form submission maps
// directly to the storage location on disk.
type Store struct {
	base string
	mu   sync.RWMutex
	ix   *Index
}

var (
	indexRegMu  sync.Mutex
	indexByBase = map[string]*Index{}
)

// Open opens (creating if needed) a store rooted at base. All stores opened
// on the same base path share one in-memory index.
func Open(base string) (*Store, error) {
	if base == "" {
		base = "./data"
	}
	abs, err := filepath.Abs(base)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil && !st.IsDir() {
		return nil, fmt.Errorf("pod: base path %q is not a directory", abs)
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		return nil, err
	}
	indexRegMu.Lock()
	ix, ok := indexByBase[abs]
	if !ok {
		ix = NewIndex()
		indexByBase[abs] = ix
	}
	indexRegMu.Unlock()
	return &Store{base: abs, ix: ix}, nil
}

// Base returns the absolute base directory of the store.
func (s *Store) Base() string { return s.base }

// NewID returns a fresh, unique record id.
func (s *Store) NewID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("rec_%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("rec_%d_%x", time.Now().UnixNano(), b)
}

// TablePath resolves and validates a table path (may contain silo segments).
func (s *Store) TablePath(table string) (string, error) {
	if table == "" {
		return "", fmt.Errorf("pod: empty table path")
	}
	if strings.Contains(table, `\`) {
		return "", fmt.Errorf("pod: backslashes are not allowed in table paths")
	}
	clean := filepath.Clean(filepath.FromSlash(table))
	if clean == "." || clean == ".." || filepath.IsAbs(clean) ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("pod: invalid table path %q", table)
	}
	return filepath.Join(s.base, clean), nil
}

// RecordPath resolves and validates the file path for a record.
func (s *Store) RecordPath(table, id string) (string, error) {
	dir, err := s.TablePath(table)
	if err != nil {
		return "", err
	}
	id, err = safeID(id)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("pod: empty record id")
	}
	return filepath.Join(dir, recordFileName(id)), nil
}

// safeID validates and cleans a record id for use as a file name.
func safeID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", nil
	}
	if id == "." || id == ".." ||
		strings.ContainsAny(id, `/\`) ||
		strings.HasPrefix(id, ".") ||
		strings.IndexFunc(id, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", fmt.Errorf("pod: invalid record id %q", id)
	}
	if len(id) > 255 {
		return "", fmt.Errorf("pod: record id too long")
	}
	return id, nil
}

// ListTables returns all table paths (relative, slash-separated, sorted).
func (s *Store) ListTables() ([]string, error) {
	var tables []string
	filepath.WalkDir(s.base, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || !d.IsDir() {
			if err == fs.ErrNotExist {
				return nil
			}
			return nil
		}
		if path == s.base || !isTableDir(path) {
			return nil
		}
		rel, rerr := filepath.Rel(s.base, path)
		if rerr != nil {
			return nil
		}
		tables = append(tables, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(tables)
	return tables, nil
}

// TableInfo returns a summary for a single table.
func (s *Store) TableInfo(table string) (*TableInfo, error) {
	ids, err := s.allIDs(table)
	if err != nil {
		return nil, err
	}
	sc, err := s.EffectiveSchema(table)
	if err != nil {
		return nil, err
	}
	return &TableInfo{Name: table, Count: len(ids), Schema: sc}, nil
}

// CreateTable creates a schema for a new table.
func (s *Store) CreateTable(table string, cols []Column) error {
	dir, err := s.TablePath(table)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	scPath := filepath.Join(dir, schemaFileName)
	if _, err := os.Stat(scPath); err == nil {
		return fmt.Errorf("pod: table %q already exists", table)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if err := saveSchema(scPath, NewSchema(table, cols)); err != nil {
		return err
	}
	s.ix.SetSynced(table)
	return nil
}

// UpdateSchema replaces the columns of an existing table (creating the table
// if needed) and bumps the schema version when the columns changed, so the
// normalizer knows to backfill older records.
func (s *Store) UpdateSchema(table string, cols []Column) error {
	dir, err := s.TablePath(table)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	scPath := filepath.Join(dir, schemaFileName)
	if _, err := os.Stat(scPath); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		if err := saveSchema(scPath, NewSchema(table, cols)); err != nil {
			return err
		}
		s.ix.SetSynced(table)
		return nil
	} else if err != nil {
		return err
	}
	data, err := os.ReadFile(scPath)
	if err != nil {
		return err
	}
	var sc Schema
	if err := xml.Unmarshal(data, &sc); err != nil {
		return err
	}
	if !schemaColumnsEqual(sc.Columns, cols) {
		sc.Columns = cols
		sc.Version++
		sc.UpdatedAt = time.Now().UTC()
		if sc.Version == 0 {
			sc.Version = 1
		}
		if err := saveSchema(scPath, &sc); err != nil {
			return err
		}
	}
	return nil
}

// DropTable removes a table directory and all its records.
func (s *Store) DropTable(table string) error {
	dir, err := s.TablePath(table)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return ErrNotFound
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	s.ix.DropTable(table)
	return nil
}

// Schema returns the schema for a table, or nil when the table has none yet.
func (s *Store) Schema(table string) (*Schema, error) {
	dir, err := s.TablePath(table)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, schemaFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var sc Schema
	if err := xml.Unmarshal(data, &sc); err != nil {
		return nil, fmt.Errorf("pod: malformed schema for %q: %w", table, err)
	}
	if sc.Columns == nil {
		sc.Columns = []Column{}
	}
	return &sc, nil
}

// EffectiveSchema returns the table schema, or derives one from existing
// records when no explicit schema has been defined.
func (s *Store) EffectiveSchema(table string) (*Schema, error) {
	if sc, err := s.Schema(table); err != nil || sc != nil {
		return sc, err
	}
	ids, err := s.allIDs(table)
	if err != nil {
		return nil, err
	}
	if len(ids) > 100 {
		ids = ids[:100]
	}
	seen := map[string]struct{}{}
	var names []string
	for _, id := range ids {
		rec, err := s.Get(table, id)
		if err != nil {
			continue
		}
		for _, f := range rec.Fields {
			if _, ok := seen[f.Name]; !ok {
				seen[f.Name] = struct{}{}
				names = append(names, f.Name)
			}
		}
	}
	sort.Strings(names)
	cols := make([]Column, 0, len(names))
	for _, name := range names {
		cols = append(cols, Column{Name: name, Type: "text"})
	}
	return NewSchema(table, cols), nil
}

// Upsert inserts or replaces a record in table. When id is empty, one is
// generated. It reports the record and whether a new record was created.
func (s *Store) Upsert(table, id string, values map[string]string) (*Record, bool, error) {
	dir, err := s.TablePath(table)
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	id, err = safeID(id)
	if err != nil {
		return nil, false, err
	}
	if id == "" {
		id = s.newID()
	}
	path := filepath.Join(dir, recordFileName(id))

	rec, loadErr := loadRecordFile(path)
	created := false
	now := time.Now().UTC().Format(timeFormat)
	if loadErr != nil {
		if !errors.Is(loadErr, fs.ErrNotExist) {
			return nil, false, loadErr
		}
		created = true
		rec = &Record{ID: id, Created: now, Updated: now, Version: 1}
		if sc, serr := s.Schema(table); serr == nil && sc != nil {
			rec.SchemaVersion = sc.Version
		}
	} else {
		rec.Version++
		rec.Updated = now
		if sc, serr := s.Schema(table); serr == nil && sc != nil {
			rec.SchemaVersion = sc.Version
		}
	}
	for k, v := range values {
		rec.Set(k, v)
	}
	if err := s.writeRecordFile(table, rec); err != nil {
		return nil, false, err
	}
	return rec, created, nil
}

// Insert inserts a new record. It fails when the id already exists.
func (s *Store) Insert(table string, values map[string]string) (*Record, error) {
	if id := values["id"]; id != "" {
		if _, err := s.Get(table, id); err == nil {
			return nil, fmt.Errorf("pod: duplicate key %q in table %q", id, table)
		}
	}
	rec, created, err := s.Upsert(table, values["id"], values)
	if err != nil {
		return nil, err
	}
	_ = created
	return rec, nil
}

// Update applies a partial update to an existing record. Changing the id is
// not allowed.
func (s *Store) Update(table, id string, values map[string]string) (*Record, error) {
	id, err := safeID(id)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("pod: update requires an id")
	}
	if _, ok := values["id"]; ok {
		return nil, fmt.Errorf("pod: cannot change the id of a record")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.RecordPath(table, id)
	if err != nil {
		return nil, err
	}
	rec, err := loadRecordFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	for k, v := range values {
		rec.Set(k, v)
	}
	rec.Version++
	rec.Updated = time.Now().UTC().Format(timeFormat)
	if sc, serr := s.Schema(table); serr == nil && sc != nil {
		rec.SchemaVersion = sc.Version
	}
	if err := s.writeRecordFile(table, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// Get loads a single record, using the index cache when it is fresh.
func (s *Store) Get(table, id string) (*Record, error) {
	id, err := safeID(id)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("pod: empty record id")
	}
	path, err := s.RecordPath(table, id)
	if err != nil {
		return nil, err
	}
	if e, ok := s.ix.Entry(table, id); ok {
		st, serr := os.Stat(path)
		if serr == nil && st.ModTime().Equal(e.MTime) && st.Size() == e.Size {
			return e.Rec, nil
		}
		if os.IsNotExist(serr) {
			s.ix.Remove(table, id)
			return nil, ErrNotFound
		}
	}
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rec, err := decodeRecord(data)
	if err != nil {
		return nil, err
	}
	s.ix.Upsert(table, id, rec, st.ModTime(), st.Size())
	return rec, nil
}

// Delete removes a record.
func (s *Store) Delete(table, id string) error {
	id, err := safeID(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.RecordPath(table, id)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	s.ix.Remove(table, id)
	return nil
}

// IDs returns the sorted ids of all records in a table.
func (s *Store) IDs(table string) ([]string, error) {
	return s.allIDs(table)
}

// Query runs a filter (Cond) over a table. When the index is synced for the
// table and the condition contains top-level equality predicates, the index
// narrows the candidate set before loading records. Results may be ordered
// and paginated via opts.
func (s *Store) Query(table string, cond Cond, opts *QueryOptions) ([]*Record, error) {
	var ids []string
	narrowed := false
	if cond != nil {
		if n, ok := s.narrowIDs(table, cond); ok {
			if len(n) == 0 {
				return []*Record{}, nil
			}
			ids, narrowed = n, true
		}
	}
	if !narrowed {
		var err error
		ids, err = s.allIDs(table)
		if err != nil {
			return nil, err
		}
	}
	recs := make([]*Record, 0, len(ids))
	for _, id := range ids {
		rec, err := s.Get(table, id)
		if err != nil {
			continue // removed concurrently
		}
		if cond == nil || cond.Match(rec) {
			recs = append(recs, rec)
		}
	}
	if opts != nil {
		if opts.OrderBy != "" {
			sort.Slice(recs, func(i, j int) bool {
				a, _ := recs[i].Get(opts.OrderBy)
				b, _ := recs[j].Get(opts.OrderBy)
				c := sortValues(a, b)
				if opts.Desc {
					return c > 0
				}
				return c < 0
			})
		}
		off := opts.Offset
		if off < 0 {
			off = 0
		}
		if len(recs) > off {
			recs = recs[off:]
		} else {
			recs = []*Record{}
		}
		if opts.Limit > 0 && len(recs) > opts.Limit {
			recs = recs[:opts.Limit]
		}
	}
	return recs, nil
}

// NormalizeRecord upgrades a single record file to the current schema,
// backfilling default values for missing columns. It reports whether the
// record was changed.
func (s *Store) NormalizeRecord(table, id string) (bool, error) {
	id, err := safeID(id)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sc, err := s.Schema(table)
	if err != nil {
		return false, err
	}
	if sc == nil || len(sc.Columns) == 0 {
		return false, nil
	}
	path, err := s.RecordPath(table, id)
	if err != nil {
		return false, err
	}
	rec, err := loadRecordFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, ErrNotFound
		}
		return false, err
	}
	changed := false
	for _, col := range sc.Columns {
		if col.Default == nil {
			continue
		}
		if _, has := rec.Get(col.Name); !has {
			rec.Set(col.Name, *col.Default)
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	rec.Version++
	rec.Updated = time.Now().UTC().Format(timeFormat)
	rec.SchemaVersion = sc.Version
	if err := s.writeRecordFile(table, rec); err != nil {
		return false, err
	}
	return true, nil
}

// writeRecordFile atomically writes a record and refreshes the index.
func (s *Store) writeRecordFile(table string, rec *Record) error {
	path, err := s.RecordPath(table, rec.ID)
	if err != nil {
		return err
	}
	data, err := encodeRecord(rec)
	if err != nil {
		return err
	}
	if err := atomicWriteFile(path, data, 0644); err != nil {
		return err
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	s.ix.Upsert(table, rec.ID, rec, st.ModTime(), st.Size())
	return nil
}

// allIDs returns the sorted record ids of a table from the index when
// available, falling back to a directory scan otherwise.
func (s *Store) allIDs(table string) ([]string, error) {
	if ids, ok := s.ix.All(table); ok {
		if len(ids) > 0 {
			return ids, nil
		}
		if s.ix.Synced(table) {
			return []string{}, nil // index is authoritative; table is empty
		}
	}
	return s.scanIDs(table)
}

// scanIDs lists record id files directly from disk.
func (s *Store) scanIDs(table string) ([]string, error) {
	dir, err := s.TablePath(table)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || !isRecordFile(e.Name()) {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".xml"))
	}
	sort.Strings(ids)
	return ids, nil
}

// narrowIDs uses the index to intersect equality predicates. ok is true when
// the returned set is trustworthy (index synced for the table).
func (s *Store) narrowIDs(table string, cond Cond) ([]string, bool) {
	if !s.ix.Synced(table) {
		return nil, false
	}
	eqs := TopLevelEqs(cond)
	if len(eqs) == 0 {
		return nil, false
	}
	candidate := map[string]struct{}{}
	first := true
	for _, e := range eqs {
		ids, ok := s.ix.Lookup(table, e.Field, e.Value)
		if !ok {
			return []string{}, true // nothing ever matched; index is authoritative
		}
		if first {
			for _, id := range ids {
				candidate[id] = struct{}{}
			}
			first = false
			continue
		}
		for id := range candidate {
			if !contains(ids, id) {
				delete(candidate, id)
			}
		}
	}
	out := make([]string, 0, len(candidate))
	for id := range candidate {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, true
}

func (s *Store) newID() string { return s.NewID() }

func contains(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

func schemaColumnsEqual(a, b []Column) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Type != b[i].Type {
			return false
		}
		ad, bd := a[i].Default, b[i].Default
		switch {
		case ad == nil && bd == nil:
		case ad == nil || bd == nil:
			return false
		case *ad != *bd:
			return false
		}
	}
	return true
}
