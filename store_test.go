package pod

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestUpsertGeneratedID(t *testing.T) {
	st := newTestStore(t)
	rec, created, err := st.Upsert("t", "", map[string]string{"name": "jane"})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected created=true for a fresh record")
	}
	if rec.ID == "" {
		t.Fatal("expected a generated id")
	}
	got, err := st.Get("t", rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := got.Get("name")
	if v != "jane" {
		t.Fatalf("name = %q, want jane", v)
	}
	if got.Version != 1 {
		t.Fatalf("version = %d, want 1", got.Version)
	}
}

func TestUpsertExistingBumpsVersion(t *testing.T) {
	st := newTestStore(t)
	if _, _, err := st.Upsert("t", "1", map[string]string{"name": "a"}); err != nil {
		t.Fatal(err)
	}
	rec, created, err := st.Upsert("t", "1", map[string]string{"name": "b", "extra": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("expected created=false for an existing record")
	}
	if rec.Version != 2 {
		t.Fatalf("version = %d, want 2", rec.Version)
	}
	v, _ := rec.Get("name")
	if v != "b" {
		t.Fatalf("name = %q, want b", v)
	}
	// the record file reflects the new values
	got, err := st.Get("t", "1")
	if err != nil {
		t.Fatal(err)
	}
	if _, has := got.Get("extra"); !has {
		t.Fatal("expected extra field persisted")
	}
}

func TestInsertDuplicateKey(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.Insert("t", map[string]string{"id": "1", "name": "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Insert("t", map[string]string{"id": "1", "name": "b"}); err == nil {
		t.Fatal("expected duplicate key error")
	}
}

func TestUpdatePartialPreservesOtherFields(t *testing.T) {
	st := newTestStore(t)
	if _, _, err := st.Upsert("t", "1", map[string]string{"name": "jane", "city": "toronto"}); err != nil {
		t.Fatal(err)
	}
	rec, err := st.Update("t", "1", map[string]string{"city": "milan"})
	if err != nil {
		t.Fatal(err)
	}
	name, _ := rec.Get("name")
	if name != "jane" {
		t.Fatalf("name = %q, want preserved jane", name)
	}
	city, _ := rec.Get("city")
	if city != "milan" {
		t.Fatalf("city = %q, want milan", city)
	}
}

func TestUpdateRejectsIDChange(t *testing.T) {
	st := newTestStore(t)
	if _, _, err := st.Upsert("t", "1", map[string]string{"name": "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Update("t", "1", map[string]string{"id": "2"}); err == nil {
		t.Fatal("expected error when changing the id column")
	}
}

func TestDeleteAndNotFound(t *testing.T) {
	st := newTestStore(t)
	if _, _, err := st.Upsert("t", "1", map[string]string{"name": "a"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete("t", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get("t", "1"); err != ErrNotFound {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
	if err := st.Delete("t", "missing"); err != ErrNotFound {
		t.Fatalf("Delete missing = %v, want ErrNotFound", err)
	}
}

func TestSiloPathsMapToDirectories(t *testing.T) {
	st := newTestStore(t)
	if _, _, err := st.Upsert("acme/contacts", "1", map[string]string{"email": "x@y.z"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(st.base, "acme", "contacts", "1.xml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("record file not at destination path: %v", err)
	}
	// the literal file name is <id>.xml
	if _, err := os.Stat(filepath.Join(st.base, "acme", "contacts", "1.xml")); err != nil {
		t.Fatal(err)
	}
	rec, err := st.Get("acme/contacts", "1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != "1" {
		t.Fatalf("id = %q, want 1", rec.ID)
	}
}

func TestTablePathTraversalRejected(t *testing.T) {
	st := newTestStore(t)
	for _, bad := range []string{"", "..", "../x", "a/../../x", "/abs", "\\evil"} {
		if _, err := st.TablePath(bad); err == nil {
			t.Errorf("TablePath(%q) allowed, want error", bad)
		}
	}
	// normalization stays inside the base
	p, err := st.TablePath("a/../b")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p, st.base) {
		t.Fatalf("TablePath escaped base: %q", p)
	}
}

func TestQueryFiltersAndPagination(t *testing.T) {
	st := newTestStore(t)
	rows := map[string]map[string]string{
		"1": {"name": "jane", "age": "30", "email": "a@x.com"},
		"2": {"name": "bob", "age": "40", "email": "b@x.com"},
		"3": {"name": "carol", "age": "50", "email": "c@y.com"},
	}
	for id, vals := range rows {
		if _, _, err := st.Upsert("people", id, vals); err != nil {
			t.Fatal(err)
		}
	}

	eq := func(field, val string) Predicate {
		return Predicate{Field: field, Op: OpEq, Value: val, HasValue: true}
	}
	// equality
	recs, err := st.Query("people", PredCond{eq("name", "bob")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].ID != "2" {
		t.Fatalf("equality query = %+v, want [2]", idsOf(recs))
	}
	// comparison (numeric aware)
	recs, err = st.Query("people", PredCond{Predicate{Field: "age", Op: OpGt, Value: "35", HasValue: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(recs); !eqSlice(got, []string{"2", "3"}) {
		t.Fatalf("age>35 = %v, want [2 3]", got)
	}
	// LIKE
	recs, err = st.Query("people", PredCond{Predicate{Field: "email", Op: OpLike, Value: "%x.com", HasValue: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(recs); !eqSlice(got, []string{"1", "2"}) {
		t.Fatalf("email LIKE %%x.com = %v, want [1 2]", got)
	}
	// AND
	cond := AndCond{L: PredCond{Predicate{Field: "email", Op: OpLike, Value: "%.com", HasValue: true}},
		R: PredCond{Predicate{Field: "age", Op: OpGe, Value: "40", HasValue: true}}}
	recs, err = st.Query("people", cond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(recs); !eqSlice(got, []string{"2", "3"}) {
		t.Fatalf("AND query = %v, want [2 3]", got)
	}
	// OR with parens
	orCond := OrCond{L: PredCond{eq("name", "jane")}, R: PredCond{eq("name", "carol")}}
	recs, err = st.Query("people", orCond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(recs); !eqSlice(got, []string{"1", "3"}) {
		t.Fatalf("OR query = %v, want [1 3]", got)
	}
	// ordering + pagination: oldest first by age desc, page of 1
	recs, err = st.Query("people", nil, &QueryOptions{OrderBy: "age", Desc: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(recs); !eqSlice(got, []string{"3", "2", "1"}) {
		t.Fatalf("order desc = %v, want [3 2 1]", got)
	}
	recs, err = st.Query("people", nil, &QueryOptions{OrderBy: "age", Desc: true, Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(recs); !eqSlice(got, []string{"2"}) {
		t.Fatalf("limit/offset = %v, want [2]", got)
	}
}

func TestStoresOnSameBaseShareIndex(t *testing.T) {
	base := t.TempDir()
	a, err := Open(base)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(base)
	if err != nil {
		t.Fatal(err)
	}
	if a.ix != b.ix {
		t.Fatal("stores on the same base must share one Index")
	}
	if _, _, err := a.Upsert("t", "1", map[string]string{"name": "shared"}); err != nil {
		t.Fatal(err)
	}
	recs, err := b.Query("t", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].ID != "1" {
		t.Fatalf("second store sees %v, want [1]", idsOf(recs))
	}
}

func TestListTablesAndDrop(t *testing.T) {
	st := newTestStore(t)
	if _, _, err := st.Upsert("a/x", "1", map[string]string{"v": "1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Upsert("b", "1", map[string]string{"v": "1"}); err != nil {
		t.Fatal(err)
	}
	tables, err := st.ListTables()
	if err != nil {
		t.Fatal(err)
	}
	if !eqSlice(tables, []string{"a/x", "b"}) {
		t.Fatalf("tables = %v, want [a/x b]", tables)
	}
	if err := st.DropTable("a/x"); err != nil {
		t.Fatal(err)
	}
	tables, _ = st.ListTables()
	if !eqSlice(tables, []string{"b"}) {
		t.Fatalf("after drop tables = %v, want [b]", tables)
	}
	if _, err := st.Get("a/x", "1"); err != ErrNotFound {
		t.Fatalf("Get dropped = %v, want ErrNotFound", err)
	}
}

func TestSchemaAndEffectiveSchema(t *testing.T) {
	st := newTestStore(t)
	if _, _, err := st.Upsert("t", "1", map[string]string{"zeta": "1", "alpha": "2"}); err != nil {
		t.Fatal(err)
	}
	sc, err := st.EffectiveSchema("t")
	if err != nil {
		t.Fatal(err)
	}
	if sc == nil || len(sc.Columns) != 2 {
		t.Fatalf("effective schema = %+v", sc)
	}
	if err := st.UpdateSchema("t", []Column{{Name: "alpha", Type: "text"}, {Name: "zeta", Type: "text"}}); err != nil {
		t.Fatal(err)
	}
	sc, err = st.Schema("t")
	if err != nil {
		t.Fatal(err)
	}
	if sc.Version != 1 {
		t.Fatalf("schema version = %d, want 1", sc.Version)
	}
}

func idsOf(recs []*Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.ID
	}
	return out
}

func eqSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
