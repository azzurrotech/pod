package pod

import (
	"database/sql/driver"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// SQLCommand represents a parsed SQL command
type SQLCommand struct {
	Operation string
	Table     string
	Columns   []string
	Values    []string
	SetPairs  map[string]string
	Where     string
}

// parseSQL breaks down a SQL string into components
func (c *PODConn) parseSQL(query string) (*SQLCommand, error) {
	cmd := &SQLCommand{}
	upperQuery := strings.ToUpper(query)
	query = strings.TrimSpace(query)

	if strings.HasPrefix(upperQuery, "SELECT") {
		cmd.Operation = "SELECT"
		return c.parseSelect(query)
	} else if strings.HasPrefix(upperQuery, "INSERT") {
		cmd.Operation = "INSERT"
		return c.parseInsert(query)
	} else if strings.HasPrefix(upperQuery, "UPDATE") {
		cmd.Operation = "UPDATE"
		return c.parseUpdate(query)
	} else if strings.HasPrefix(upperQuery, "DELETE") {
		cmd.Operation = "DELETE"
		return c.parseDelete(query)
	}

	return nil, fmt.Errorf("unsupported SQL operation: %s", query)
}

func (c *PODConn) parseSelect(query string) (*SQLCommand, error) {
	cmd := &SQLCommand{Operation: "SELECT"}
	selectPattern := regexp.MustCompile(`(?i)SELECT\s+(.+?)\s+FROM\s+(\w+)(?:\s+WHERE\s+(.+))?$`)
	matches := selectPattern.FindStringSubmatch(query)

	if len(matches) < 3 {
		return nil, fmt.Errorf("invalid SELECT syntax")
	}

	if matches[1] == "*" {
		cmd.Columns = []string{"*"}
	} else {
		cmd.Columns = strings.Split(matches[1], ",")
		for i := range cmd.Columns {
			cmd.Columns[i] = strings.TrimSpace(cmd.Columns[i])
		}
	}

	cmd.Table = matches[2]
	if len(matches) > 3 && matches[3] != "" {
		cmd.Where = matches[3]
	}

	return cmd, nil
}

func (c *PODConn) parseInsert(query string) (*SQLCommand, error) {
	cmd := &SQLCommand{Operation: "INSERT"}
	insertPattern := regexp.MustCompile(`(?i)INSERT\s+INTO\s+(\w+)\s*\(([^)]+)\)\s*VALUES\s*\(([^)]+)\)`)
	matches := insertPattern.FindStringSubmatch(query)

	if len(matches) < 4 {
		return nil, fmt.Errorf("invalid INSERT syntax")
	}

	cmd.Table = matches[1]
	cmd.Columns = strings.Split(matches[2], ",")
	for i := range cmd.Columns {
		cmd.Columns[i] = strings.TrimSpace(cmd.Columns[i])
	}

	rawValues := strings.Split(matches[3], ",")
	for _, v := range rawValues {
		v = strings.TrimSpace(v)
		v = strings.Trim(v, "'\"")
		cmd.Values = append(cmd.Values, v)
	}

	return cmd, nil
}

func (c *PODConn) parseUpdate(query string) (*SQLCommand, error) {
	cmd := &SQLCommand{Operation: "UPDATE"}
	updatePattern := regexp.MustCompile(`(?i)UPDATE\s+(\w+)\s+SET\s+(.+?)\s+WHERE\s+(.+)$`)
	matches := updatePattern.FindStringSubmatch(query)

	if len(matches) < 4 {
		return nil, fmt.Errorf("invalid UPDATE syntax")
	}

	cmd.Table = matches[1]
	cmd.Where = matches[3]
	cmd.SetPairs = make(map[string]string)

	pairs := strings.Split(matches[2], ",")
	for _, pair := range pairs {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) == 2 {
			key := strings.TrimSpace(kv[0])
			val := strings.TrimSpace(kv[1])
			val = strings.Trim(val, "'\"")
			cmd.SetPairs[key] = val
		}
	}

	return cmd, nil
}

func (c *PODConn) parseDelete(query string) (*SQLCommand, error) {
	cmd := &SQLCommand{Operation: "DELETE"}
	deletePattern := regexp.MustCompile(`(?i)DELETE\s+FROM\s+(\w+)\s+WHERE\s+(.+)$`)
	matches := deletePattern.FindStringSubmatch(query)

	if len(matches) < 3 {
		return nil, fmt.Errorf("invalid DELETE syntax")
	}

	cmd.Table = matches[1]
	cmd.Where = matches[2]

	return cmd, nil
}

func (c *PODConn) executeInsert(db *Database, cmd *SQLCommand) (driver.Result, error) {
	newID := c.store.getNextID(db)
	record := Record{ID: newID}

	for i, col := range cmd.Columns {
		var val string
		if i < len(cmd.Values) {
			val = cmd.Values[i]
		}
		record.Fields = append(record.Fields, Field{Name: col, Value: val})
	}

	db.Records = append(db.Records, record)
	c.store.updateIndexForInsert(record)

	if err := c.store.saveDB(db); err != nil {
		return nil, err
	}

	return &PODResult{lastInsertID: int64(newID), rowsAffected: 1}, nil
}

func (c *PODConn) executeUpdate(db *Database, cmd *SQLCommand) (driver.Result, error) {
	updatedCount := 0

	for i := range db.Records {
		if c.matchesWhere(db.Records[i], cmd.Where) {
			oldFields := make(map[string]string)
			for _, f := range db.Records[i].Fields {
				oldFields[f.Name] = f.Value
			}

			for j := range db.Records[i].Fields {
				if newVal, ok := cmd.SetPairs[db.Records[i].Fields[j].Name]; ok {
					db.Records[i].Fields[j].Value = newVal
					updatedCount++
				}
			}

			c.store.updateIndexForUpdate(db.Records[i], oldFields)
		}
	}

	if err := c.store.saveDB(db); err != nil {
		return nil, err
	}

	return &PODResult{rowsAffected: int64(updatedCount)}, nil
}

func (c *PODConn) executeDelete(db *Database, cmd *SQLCommand) (driver.Result, error) {
	deletedCount := 0

	for i := len(db.Records) - 1; i >= 0; i-- {
		if c.matchesWhere(db.Records[i], cmd.Where) {
			c.store.updateIndexForDelete(db.Records[i].ID)
			db.Records = append(db.Records[:i], db.Records[i+1:]...)
			deletedCount++
		}
	}

	if err := c.store.saveDB(db); err != nil {
		return nil, err
	}

	return &PODResult{rowsAffected: int64(deletedCount)}, nil
}

func (c *PODConn) executeSelect(db *Database, cmd *SQLCommand) (driver.Rows, error) {
	var filtered []Record

	for _, record := range db.Records {
		if cmd.Where == "" || c.matchesWhere(record, cmd.Where) {
			filtered = append(filtered, record)
		}
	}

	var columns []string
	if len(cmd.Columns) == 1 && cmd.Columns[0] == "*" {
		if len(filtered) > 0 {
			for _, field := range filtered[0].Fields {
				columns = append(columns, field.Name)
			}
			// Ensure ID is first if not present
			hasID := false
			for _, col := range columns {
				if col == "id" {
					hasID = true
					break
				}
			}
			if !hasID {
				columns = append([]string{"id"}, columns...)
			}
		} else {
			columns = []string{"id"}
		}
	} else {
		columns = cmd.Columns
	}

	return &PODRows{records: filtered, columns: columns, cursor: 0}, nil
}

// matchesWhere checks if a record matches the WHERE clause
func (c *PODConn) matchesWhere(record Record, whereClause string) bool {
	if whereClause == "" {
		return true
	}

	conditions := strings.Split(whereClause, "AND")

	for _, cond := range conditions {
		cond = strings.TrimSpace(cond)

		// Optimization: Check for simple equality to use index
		if strings.Contains(cond, "=") && !strings.Contains(cond, ">") && !strings.Contains(cond, "<") {
			parts := strings.SplitN(cond, "=", 2)
			if len(parts) == 2 {
				fieldName := strings.TrimSpace(parts[0])
				expectedValue := strings.TrimSpace(parts[1])
				expectedValue = strings.Trim(expectedValue, "'\"")

				// Use index for exact match
				if ids := c.store.fastLookup(fieldName, expectedValue); ids != nil {
					for _, id := range ids {
						if id == record.ID {
							return true
						}
					}
					return false
				}
			}
		}

		// Fallback to standard parsing
		parts := strings.SplitN(cond, "=", 2)
		if len(parts) != 2 {
			continue
		}

		fieldName := strings.TrimSpace(parts[0])
		expectedValue := strings.TrimSpace(parts[1])
		expectedValue = strings.Trim(expectedValue, "'\"")

		// Check for comparison operators
		if strings.Contains(cond, ">=") || strings.Contains(cond, "<=") ||
			strings.Contains(cond, ">") || strings.Contains(cond, "<") {
			return c.matchesComparison(record, cond)
		}

		// Exact match fallback
		for _, field := range record.Fields {
			if field.Name == fieldName && field.Value == expectedValue {
				return true
			}
		}
	}

	return false
}

func (c *PODConn) matchesComparison(record Record, condition string) bool {
	var fieldName, operator, value string

	if strings.Contains(condition, ">=") {
		parts := strings.Split(condition, ">=")
		fieldName = strings.TrimSpace(parts[0])
		value = strings.TrimSpace(parts[1])
		operator = ">="
	} else if strings.Contains(condition, "<=") {
		parts := strings.Split(condition, "<=")
		fieldName = strings.TrimSpace(parts[0])
		value = strings.TrimSpace(parts[1])
		operator = "<="
	} else if strings.Contains(condition, ">") {
		parts := strings.Split(condition, ">")
		fieldName = strings.TrimSpace(parts[0])
		value = strings.TrimSpace(parts[1])
		operator = ">"
	} else if strings.Contains(condition, "<") {
		parts := strings.Split(condition, "<")
		fieldName = strings.TrimSpace(parts[0])
		value = strings.TrimSpace(parts[1])
		operator = "<"
	} else {
		return false
	}

	var fieldValue string
	for _, field := range record.Fields {
		if field.Name == fieldName {
			fieldValue = field.Value
			break
		}
	}

	if fieldValue == "" {
		return false
	}

	fieldNum, err1 := strconv.Atoi(fieldValue)
	valNum, err2 := strconv.Atoi(value)

	if err1 == nil && err2 == nil {
		switch operator {
		case ">":
			return fieldNum > valNum
		case "<":
			return fieldNum < valNum
		case ">=":
			return fieldNum >= valNum
		case "<=":
			return fieldNum <= valNum
		}
	}

	switch operator {
	case ">":
		return fieldValue > value
	case "<":
		return fieldValue < value
	case ">=":
		return fieldValue >= value
	case "<=":
		return fieldValue <= value
	}

	return false
}
