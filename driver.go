package pod

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
)

// Driver implements database/sql/driver.Driver. The data source name is the
// base directory of the store; it is created if missing.
//
//	db, _ := sql.Open("pod", "/path/to/data")
//	defer db.Close()
//
// Supported statements: CREATE TABLE, DROP TABLE, INSERT, UPSERT,
// SELECT (WHERE / ORDER BY / LIMIT / OFFSET), UPDATE, DELETE.
//
// Transactions are autocommit: each statement is atomic on its own; Commit and
// Rollback are no-ops. This matches the single-writer filesystem semantics.
type Driver struct{}

// Open creates a new connection to the store rooted at name.
func (d *Driver) Open(name string) (driver.Conn, error) {
	store, err := Open(name)
	if err != nil {
		return nil, err
	}
	return &Conn{store: store}, nil
}

// Conn is a database/sql connection backed by a Store.
type Conn struct {
	store *Store
}

// Prepare parses a statement into a reusable prepared statement.
func (c *Conn) Prepare(query string) (driver.Stmt, error) {
	st, err := Parse(query)
	if err != nil {
		return nil, err
	}
	return &Stmt{conn: c, st: st, numArgs: st.NumArgs}, nil
}

// Close is a no-op; the store stays open for other connections.
func (c *Conn) Close() error { return nil }

// Begin starts a logical transaction. Statements are executed in autocommit
// mode; Commit and Rollback are no-ops.
func (c *Conn) Begin() (driver.Tx, error) { return &Tx{}, nil }

// QueryContext executes a SELECT without a prepared statement.
func (c *Conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	vals := namedValues(args)
	return c.executeQuery(query, vals)
}

// ExecContext executes a non-SELECT statement without a prepared statement.
func (c *Conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	vals := namedValues(args)
	return c.executeExec(query, vals)
}

// IsValid reports the connection is usable. pod connections are stateless, so
// they always validate.
func (c *Conn) IsValid() bool { return true }

// ResetSession prepares the connection for reuse from a pool.
func (c *Conn) ResetSession(ctx context.Context) error { return nil }

func (c *Conn) executeQuery(query string, args []driver.Value) (driver.Rows, error) {
	st, err := Parse(query)
	if err != nil {
		return nil, err
	}
	_, rows, err := c.store.Execute(st, args)
	return rows, err
}

func (c *Conn) executeExec(query string, args []driver.Value) (driver.Result, error) {
	st, err := Parse(query)
	if err != nil {
		return nil, err
	}
	res, _, err := c.store.Execute(st, args)
	return res, err
}

// Stmt is a prepared statement.
type Stmt struct {
	conn    *Conn
	st      *Statement
	numArgs int
}

// Close releases the statement.
func (s *Stmt) Close() error { return nil }

// NumInput returns the number of '?' placeholders in the statement.
func (s *Stmt) NumInput() int { return s.numArgs }

// Exec executes the statement (non-SELECT).
func (s *Stmt) Exec(args []driver.Value) (driver.Result, error) {
	if len(args) != s.numArgs {
		return nil, driver.ErrSkip // let database/sql report the arg mismatch
	}
	res, _, err := s.conn.store.Execute(s.st, args)
	return res, err
}

// Query executes a SELECT.
func (s *Stmt) Query(args []driver.Value) (driver.Rows, error) {
	if len(args) != s.numArgs {
		return nil, driver.ErrSkip
	}
	_, rows, err := s.conn.store.Execute(s.st, args)
	return rows, err
}

// Rows is the result set of a SELECT.
type Rows struct {
	cols []string
	recs []*Record
	pos  int
}

// Columns returns the column names of the result set.
func (r *Rows) Columns() []string { return r.cols }

// Close releases the rows.
func (r *Rows) Close() error { return nil }

// Next populates dest with the next row, or returns io.EOF at the end.
func (r *Rows) Next(dest []driver.Value) error {
	if r.pos >= len(r.recs) {
		return io.EOF
	}
	rec := r.recs[r.pos]
	r.pos++
	for i := range dest {
		if i >= len(r.cols) {
			dest[i] = nil
			continue
		}
		col := r.cols[i]
		if col == "id" {
			dest[i] = rec.ID
			continue
		}
		if v, ok := rec.Get(col); ok {
			dest[i] = v
		} else {
			dest[i] = nil
		}
	}
	return nil
}

// Result carries metadata about a DML/DDL statement.
type Result struct {
	lastID   int64
	affected int64
}

// LastInsertId returns the numeric form of the inserted record id (0 when the
// id is not numeric).
func (r *Result) LastInsertId() (int64, error) { return r.lastID, nil }

// RowsAffected returns the number of rows affected.
func (r *Result) RowsAffected() (int64, error) { return r.affected, nil }

// Tx is a logical transaction with autocommit semantics.
type Tx struct{}

// Commit is a no-op (statements were committed as they ran).
func (t *Tx) Commit() error { return nil }

// Rollback is a no-op (statements were committed as they ran).
func (t *Tx) Rollback() error { return nil }

func namedValues(args []driver.NamedValue) []driver.Value {
	if len(args) == 0 {
		return nil
	}
	vals := make([]driver.Value, len(args))
	for i, a := range args {
		vals[i] = a.Value
	}
	return vals
}

// Register the driver under the name "pod".
func init() {
	sql.Register("pod", &Driver{})
}
