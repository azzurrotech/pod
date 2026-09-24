package pod

import (
	"database/sql"
	"testing"
)

func TestSQLDriverRoundTrip(t *testing.T) {
	db, err := sql.Open("pod", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	exec := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}

	// DDL
	exec("CREATE TABLE people (id TEXT, name TEXT, age TEXT)")

	// literals and placeholders
	exec("INSERT INTO people (id, name, age) VALUES ('1', 'jane', '30')")
	exec("INSERT INTO people (id, name, age) VALUES (?, ?, ?)", "2", "bob", "40")
	exec("INSERT INTO people (id, name) VALUES (?, ?)", "3", "carol")

	// SELECT *
	rows, err := db.Query("SELECT * FROM people")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range cols {
		seen[c] = true
	}
	for _, want := range []string{"id", "name", "age"} {
		if !seen[want] {
			t.Fatalf("SELECT * cols = %v, missing %q", cols, want)
		}
	}
	count := 0
	for rows.Next() {
		var id, name, age sql.NullString
		if err := rows.Scan(&id, &name, &age); err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 3 {
		t.Fatalf("SELECT * rows = %d, want 3", count)
	}

	// WHERE with comparison + placeholder
	rows, err = db.Query("SELECT name FROM people WHERE age > ?", "32")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	rows.Close()
	if len(names) != 1 || names[0] != "bob" {
		t.Fatalf("age>32 names = %v, want [bob]", names)
	}

	// LIKE
	rows, err = db.Query("SELECT id FROM people WHERE name LIKE ? ORDER BY id ASC", "%o%")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 2 || ids[0] != "2" || ids[1] != "3" {
		t.Fatalf("LIKE ids = %v, want [2 3]", ids)
	}

	// ORDER BY + LIMIT
	rows, err = db.Query("SELECT id FROM people ORDER BY age DESC LIMIT 1")
	if err != nil {
		t.Fatal(err)
	}
	rows.Next()
	var top string
	if err := rows.Scan(&top); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if top != "2" {
		t.Fatalf("oldest = %q, want 2", top)
	}

	// UPDATE
	res, err := db.Exec("UPDATE people SET age = ? WHERE name = ?", "49", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("update affected = %d, want 1", n)
	}
	rows, err = db.Query("SELECT age FROM people WHERE id = ?", "2")
	if err != nil {
		t.Fatal(err)
	}
	rows.Next()
	var age string
	rows.Scan(&age)
	rows.Close()
	if age != "49" {
		t.Fatalf("age after update = %q, want 49", age)
	}

	// DELETE
	res, err = db.Exec("DELETE FROM people WHERE name = ?", "carol")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("delete affected = %d, want 1", n)
	}

	// UPSERT overwrites
	exec("UPSERT INTO people (id, name, age) VALUES (?, ?, ?)", "1", "jane doe", "31")
	rows, err = db.Query("SELECT name FROM people WHERE id = ?", "1")
	if err != nil {
		t.Fatal(err)
	}
	rows.Next()
	var name string
	rows.Scan(&name)
	rows.Close()
	if name != "jane doe" {
		t.Fatalf("upsert name = %q, want jane doe", name)
	}
}

func TestSQLDriverDuplicateInsert(t *testing.T) {
	db, err := sql.Open("pod", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t (id TEXT, v TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO t (id, v) VALUES ('1', 'a')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO t (id, v) VALUES ('1', 'b')"); err == nil {
		t.Fatal("expected duplicate key error on second INSERT")
	}
}

func TestSQLDriverAutocommitTransaction(t *testing.T) {
	db, err := sql.Open("pod", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t (id TEXT, v TEXT)"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO t (id, v) VALUES ('1', 'a')"); err != nil {
		t.Fatal(err)
	}
	// statements commit as they run
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("SELECT v FROM t WHERE id = ?", "1")
	if err != nil {
		t.Fatal(err)
	}
	rows.Next()
	var v string
	if err := rows.Scan(&v); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if v != "a" {
		t.Fatalf("v = %q, want a", v)
	}
}

func TestSQLDriverPreparedStatement(t *testing.T) {
	db, err := sql.Open("pod", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t (id TEXT, v TEXT)"); err != nil {
		t.Fatal(err)
	}
	stmt, err := db.Prepare("INSERT INTO t (id, v) VALUES (?, ?)")
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	if _, err := stmt.Exec("k1", "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := stmt.Exec("k2", "v2"); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("SELECT v FROM t ORDER BY id ASC")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	rows.Close()
	if len(got) != 2 || got[0] != "v1" || got[1] != "v2" {
		t.Fatalf("prepared insert results = %v, want [v1 v2]", got)
	}
}

func TestSQLDriverTwoConnectionsShareStore(t *testing.T) {
	base := t.TempDir()
	a, err := sql.Open("pod", base)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := sql.Open("pod", base)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := a.Exec("CREATE TABLE t (id TEXT, v TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec("INSERT INTO t (id, v) VALUES ('1', 'x')"); err != nil {
		t.Fatal(err)
	}
	rows, err := b.Query("SELECT v FROM t WHERE id = ?", "1")
	if err != nil {
		t.Fatal(err)
	}
	rows.Next()
	var v string
	if err := rows.Scan(&v); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if v != "x" {
		t.Fatalf("second connection read v = %q, want x", v)
	}
}
