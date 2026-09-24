package pod

import (
	"os"
	"path/filepath"
	"testing"
)

func strPtr(s string) *string { return &s }

func TestNormalizeBackfillsSchemaDefaults(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpdateSchema("t", []Column{{Name: "name", Type: "text"}}); err != nil {
		t.Fatal(err)
	}
	// record without the future "city" column
	if _, _, err := st.Upsert("t", "1", map[string]string{"name": "jane"}); err != nil {
		t.Fatal(err)
	}
	// schema upgrade: new column with a default
	if err := st.UpdateSchema("t", []Column{
		{Name: "name", Type: "text"},
		{Name: "city", Type: "text", Default: strPtr("NYC")},
	}); err != nil {
		t.Fatal(err)
	}
	changed, err := st.NormalizeRecord("t", "1")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected the record to be normalized")
	}
	rec, err := st.Get("t", "1")
	if err != nil {
		t.Fatal(err)
	}
	v, ok := rec.Get("city")
	if !ok || v != "NYC" {
		t.Fatalf("city = %q (present %v), want NYC", v, ok)
	}
	if _, has := rec.Get("name"); !has {
		t.Fatal("existing name field must be preserved")
	}
	// second run is a no-op
	changed, err = st.NormalizeRecord("t", "1")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("already normalized record should not change")
	}
}

func TestNormalizeDoesNotOverwriteExistingValues(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpdateSchema("t", []Column{{Name: "v", Type: "text"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Upsert("t", "1", map[string]string{"v": "custom"}); err != nil {
		t.Fatal(err)
	}
	// upgrade with a default for v: existing value wins
	if err := st.UpdateSchema("t", []Column{{Name: "v", Type: "text", Default: strPtr("DEFAULT")}}); err != nil {
		t.Fatal(err)
	}
	changed, err := st.NormalizeRecord("t", "1")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("existing value must not be overwritten")
	}
	rec, _ := st.Get("t", "1")
	if v, _ := rec.Get("v"); v != "custom" {
		t.Fatalf("v = %q, want custom", v)
	}
}

func TestNormalizerRunOnceScansAllTables(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpdateSchema("t1", []Column{{Name: "a"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateSchema("t2", []Column{{Name: "a"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Upsert("t1", "1", map[string]string{"a": "x"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Upsert("t2", "1", map[string]string{"a": "y"}); err != nil {
		t.Fatal(err)
	}
	// upgrade both schemas adding a "flag" column with a default
	for _, table := range []string{"t1", "t2"} {
		if err := st.UpdateSchema(table, []Column{
			{Name: "a"},
			{Name: "flag", Type: "text", Default: strPtr("off")},
		}); err != nil {
			t.Fatal(err)
		}
	}
	n := NewNormalizer(st)
	count, err := n.RunOnce()
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("RunOnce normalized %d, want 2", count)
	}
	for _, table := range []string{"t1", "t2"} {
		rec, err := st.Get(table, "1")
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := rec.Get("flag"); v != "off" {
			t.Fatalf("%s flag = %q, want off", table, v)
		}
	}
	if _, n := n.Status(); n != 2 {
		t.Fatalf("status count = %d, want 2", n)
	}
}

func TestNormalizeMissingRecordReturnsNotFound(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpdateSchema("t", []Column{{Name: "a", Default: strPtr("d")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.NormalizeRecord("t", "nope"); err != ErrNotFound {
		t.Fatalf("NormalizeRecord missing = %v, want ErrNotFound", err)
	}
}

func TestIndexerSyncTracksExternalFiles(t *testing.T) {
	st := newTestStore(t)
	if err := st.CreateTable("t", []Column{{Name: "name", Type: "text"}}); err != nil {
		t.Fatal(err)
	}
	// write a record file directly on disk, bypassing the store
	dir, err := st.TablePath("t")
	if err != nil {
		t.Fatal(err)
	}
	rec := &Record{
		ID:      "ext",
		Version: 1,
		Created: "2026-01-01T00:00:00Z",
		Updated: "2026-01-01T00:00:00Z",
		Fields:  []Field{{Name: "name", Value: "external"}},
	}
	data, err := encodeRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ext.xml"), data, 0644); err != nil {
		t.Fatal(err)
	}

	ix := NewIndexer(st)
	if err := ix.Sync(); err != nil {
		t.Fatal(err)
	}
	if !st.ix.Synced("t") {
		t.Fatal("table should be synced after indexer pass")
	}
	// equality query now narrows via the index and finds the external record
	recs, err := st.Query("t", PredCond{Predicate{Field: "name", Op: OpEq, Value: "external", HasValue: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].ID != "ext" {
		t.Fatalf("query after sync = %v, want [ext]", idsOf(recs))
	}
	// plain listing finds it too
	if ids, err := st.IDs("t"); err != nil || !eqSlice(ids, []string{"ext"}) {
		t.Fatalf("IDs = %v, %v; want [ext]", ids, err)
	}

	// remove the file on disk; sync must drop it from the index
	if err := os.Remove(filepath.Join(dir, "ext.xml")); err != nil {
		t.Fatal(err)
	}
	if err := ix.Sync(); err != nil {
		t.Fatal(err)
	}
	recs, err = st.Query("t", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("after delete query = %v, want empty", idsOf(recs))
	}
}

func TestIndexerRebuild(t *testing.T) {
	st := newTestStore(t)
	if _, _, err := st.Upsert("t", "1", map[string]string{"v": "a"}); err != nil {
		t.Fatal(err)
	}
	// write a second file behind the store's back
	dir, _ := st.TablePath("t")
	ext := &Record{ID: "ext2", Version: 1, Fields: []Field{{Name: "v", Value: "b"}}}
	data, _ := encodeRecord(ext)
	if err := os.WriteFile(filepath.Join(dir, "ext2.xml"), data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := NewIndexer(st).Rebuild(); err != nil {
		t.Fatal(err)
	}
	ids, err := st.IDs("t")
	if err != nil {
		t.Fatal(err)
	}
	if !eqSlice(ids, []string{"1", "ext2"}) {
		t.Fatalf("IDs after rebuild = %v, want [1 ext2]", ids)
	}
}

func TestIndexerDropsRemovedTables(t *testing.T) {
	st := newTestStore(t)
	if _, _, err := st.Upsert("t", "1", map[string]string{"v": "a"}); err != nil {
		t.Fatal(err)
	}
	ix := NewIndexer(st)
	if err := ix.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(st.base + "/t"); err != nil {
		t.Fatal(err)
	}
	if err := ix.Sync(); err != nil {
		t.Fatal(err)
	}
	if st.ix.Synced("t") {
		t.Fatal("removed table should leave the index")
	}
	if _, ok := st.ix.All("t"); ok {
		t.Fatal("removed table ids should be gone from the index")
	}
}
