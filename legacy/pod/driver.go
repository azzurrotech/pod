package pod

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
)

// PODDriver implements the driver.Driver interface
type PODDriver struct{}

// Open creates a new connection to the database
func (d *PODDriver) Open(name string) (driver.Conn, error) {
	// Use the indexed store
	store := NewStoreWithIndex(name)
	return &PODConn{store: store}, nil
}

// PODConn implements the driver.Conn interface
type PODConn struct {
	store *PODStoreWithIndex
}

// Prepare creates a prepared statement for later usage
func (c *PODConn) Prepare(query string) (driver.Stmt, error) {
	// Count the number of '?' placeholders
	args := strings.Count(query, "?")
	return &PODStmt{
		conn:    c,
		query:   query,
		numArgs: args,
	}, nil
}

// Close closes the connection
func (c *PODConn) Close() error {
	return nil
}

// Begin starts a transaction (not supported in this V1 implementation)
func (c *PODConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported in POD V1")
}

// ExecContext executes a query that doesn't return rows
func (c *PODConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	values := make([]driver.Value, len(args))
	for i, v := range args {
		values[i] = v.Value
	}
	return c.Exec(query, values)
}

// QueryContext executes a query that returns rows
func (c *PODConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	values := make([]driver.Value, len(args))
	for i, v := range args {
		values[i] = v.Value
	}
	return c.Query(query, values)
}

// Exec executes a query that doesn't return rows (Direct execution without prepared statement)
func (c *PODConn) Exec(query string, args []driver.Value) (driver.Result, error) {
	// Replace placeholders
	query = c.replacePlaceholders(query, args)

	// Parse and execute
	cmd, err := c.parseSQL(query)
	if err != nil {
		return nil, err
	}

	db, err := c.store.loadDB()
	if err != nil {
		return nil, err
	}

	switch cmd.Operation {
	case "INSERT":
		return c.executeInsert(db, cmd)
	case "UPDATE":
		return c.executeUpdate(db, cmd)
	case "DELETE":
		return c.executeDelete(db, cmd)
	default:
		return nil, fmt.Errorf("operation %s does not return a result", cmd.Operation)
	}
}

// Query executes a query that returns rows (Direct execution without prepared statement)
func (c *PODConn) Query(query string, args []driver.Value) (driver.Rows, error) {
	// Replace placeholders
	query = c.replacePlaceholders(query, args)

	// Parse and execute
	cmd, err := c.parseSQL(query)
	if err != nil {
		return nil, err
	}

	if cmd.Operation != "SELECT" {
		return nil, fmt.Errorf("expected SELECT, got %s", cmd.Operation)
	}

	db, err := c.store.loadDB()
	if err != nil {
		return nil, err
	}

	return c.executeSelect(db, cmd)
}

// PODStmt implements the driver.Stmt interface
type PODStmt struct {
	conn    *PODConn
	query   string
	numArgs int
}

// Close closes the statement
func (s *PODStmt) Close() error {
	return nil
}

// NumInput returns the number of placeholder parameters
func (s *PODStmt) NumInput() int {
	return s.numArgs
}

// Exec executes a query that doesn't return rows
func (s *PODStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.conn.Exec(s.query, args)
}

// Query executes a query that returns rows
func (s *PODStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.conn.Query(s.query, args)
}

// PODResult implements the driver.Result interface
type PODResult struct {
	lastInsertID int64
	rowsAffected int64
}

// LastInsertId returns the database's auto-incremented ID
func (r *PODResult) LastInsertId() (int64, error) {
	return r.lastInsertID, nil
}

// RowsAffected returns the number of rows affected by the query
func (r *PODResult) RowsAffected() (int64, error) {
	return r.rowsAffected, nil
}

// PODRows implements the driver.Rows interface
type PODRows struct {
	records []Record
	columns []string
	cursor  int
}

// Columns returns the names of the columns
func (r *PODRows) Columns() []string {
	return r.columns
}

// Next is called to populate the next row of data into dest
func (r *PODRows) Next(dest []driver.Value) error {
	if r.cursor >= len(r.records) {
		return io.EOF
	}

	record := r.records[r.cursor]
	r.cursor++

	// Map columns to values
	for i := range dest {
		if i < len(r.columns) {
			colName := r.columns[i]
			found := false
			for _, field := range record.Fields {
				if field.Name == colName {
					dest[i] = field.Value
					found = true
					break
				}
			}
			if !found {
				dest[i] = nil
			}
		} else {
			dest[i] = nil
		}
	}

	return nil
}

// Close closes the rows iterator
func (r *PODRows) Close() error {
	return nil
}

// replacePlaceholders replaces '?' with quoted values from args
func (c *PODConn) replacePlaceholders(query string, args []driver.Value) string {
	result := query
	for _, arg := range args {
		valStr := fmt.Sprintf("%v", arg)
		// Escape single quotes for SQL safety
		valStr = strings.ReplaceAll(valStr, "'", "''")
		// Replace the first occurrence of ?
		result = strings.Replace(result, "?", fmt.Sprintf("'%s'", valStr), 1)
	}
	return result
}
