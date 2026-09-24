package pod

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// schemaFileName is the hidden file holding a table's column definitions.
const schemaFileName = ".pod-schema.xml"

// Column describes one field of a table.
type Column struct {
	Name    string  `xml:"name,attr"`
	Type    string  `xml:"type,attr,omitempty"`
	Default *string `xml:"default,omitempty"`
}

// Schema is the column definition for a table. It lives at
// <table-dir>/.pod-schema.xml and is what the normalizer compares records
// against: columns introduced in a newer schema version are backfilled with
// their default value into older XML records.
type Schema struct {
	XMLName   xml.Name  `xml:"schema"`
	Table     string    `xml:"table,attr"`
	Version   int       `xml:"version,attr"`
	Columns   []Column  `xml:"column"`
	CreatedAt time.Time `xml:"created_at,attr,omitempty"`
	UpdatedAt time.Time `xml:"updated_at,attr,omitempty"`
}

// NewSchema builds a schema with the given columns at version 1.
func NewSchema(table string, cols []Column) *Schema {
	now := time.Now().UTC()
	return &Schema{
		Table:     table,
		Version:   1,
		Columns:   cols,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// Column returns the column with the given name, or nil.
func (s *Schema) Column(name string) *Column {
	for i := range s.Columns {
		if s.Columns[i].Name == name {
			return &s.Columns[i]
		}
	}
	return nil
}

// HasDefault reports whether a column defines a default value.
func (s *Schema) HasDefault(name string) (string, bool) {
	if s == nil {
		return "", false
	}
	if c := s.Column(name); c != nil && c.Default != nil {
		return *c.Default, true
	}
	return "", false
}

// ApplyDefaults fills in any values whose keys are missing but whose columns
// declare a default. Existing values are never overwritten.
func (s *Schema) ApplyDefaults(values map[string]string) {
	if s == nil {
		return
	}
	for _, c := range s.Columns {
		if c.Default == nil {
			continue
		}
		if _, ok := values[c.Name]; !ok {
			values[c.Name] = *c.Default
		}
	}
}

// schemaForRecord derives an implicit schema from a record's fields.
func schemaForRecord(table string, r *Record) *Schema {
	cols := make([]Column, 0, len(r.Fields))
	for _, f := range r.Fields {
		cols = append(cols, Column{Name: f.Name, Type: "text"})
	}
	return NewSchema(table, cols)
}

// saveSchema writes the schema file atomically.
func saveSchema(path string, s *Schema) error {
	body, err := xml.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	out := append([]byte(xml.Header), body...)
	out = append(out, '\n')
	return atomicWriteFile(path, out, 0644)
}

func loadSchema(path string) (*Schema, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Schema
	if err := xml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("pod: malformed schema %s: %w", path, err)
	}
	return &s, nil
}

// atomicWriteFile writes data to a temp file in the target directory and
// renames it over the destination, so readers never observe a partial file.
func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".pod-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
