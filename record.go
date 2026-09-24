package pod

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"sort"
)

// timeFormat is the canonical timestamp layout used for record metadata.
const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"

// Field is a single key/value pair inside a record.
type Field struct {
	Name  string `xml:"name,attr"`
	Value string `xml:",chardata"`
}

// Record is one row of the database. It serializes to and from a single XML
// file of the form:
//
//	<record id="..." schema_version="1" version="2" created="..." updated="...">
//	  <field name="email">jane@example.com</field>
//	</record>
type Record struct {
	XMLName       xml.Name `xml:"record"`
	ID            string   `xml:"id,attr"`
	SchemaVersion int      `xml:"schema_version,attr,omitempty"`
	Version       int      `xml:"version,attr,omitempty"`
	Created       string   `xml:"created,attr,omitempty"`
	Updated       string   `xml:"updated,attr,omitempty"`
	Fields        []Field  `xml:"field"`
}

// Values returns the record fields as a map.
func (r *Record) Values() map[string]string {
	m := make(map[string]string, len(r.Fields))
	for _, f := range r.Fields {
		m[f.Name] = f.Value
	}
	return m
}

// Get returns the value of a field and whether the field exists.
func (r *Record) Get(name string) (string, bool) {
	for i := range r.Fields {
		if r.Fields[i].Name == name {
			return r.Fields[i].Value, true
		}
	}
	return "", false
}

// Set inserts or replaces a field. It reports whether the value changed.
// Missing fields are appended, preserving existing field order.
func (r *Record) Set(name, value string) bool {
	for i := range r.Fields {
		if r.Fields[i].Name == name {
			if r.Fields[i].Value == value {
				return false
			}
			r.Fields[i].Value = value
			return true
		}
	}
	r.Fields = append(r.Fields, Field{Name: name, Value: value})
	return true
}

// DeleteField removes a field if present and reports whether it was removed.
func (r *Record) DeleteField(name string) bool {
	for i := range r.Fields {
		if r.Fields[i].Name == name {
			r.Fields = append(r.Fields[:i], r.Fields[i+1:]...)
			return true
		}
	}
	return false
}

// ColumnNames returns the ordered field names of the record.
func (r *Record) ColumnNames() []string {
	names := make([]string, 0, len(r.Fields))
	for _, f := range r.Fields {
		names = append(names, f.Name)
	}
	return names
}

// Clone returns a deep copy of the record.
func (r *Record) Clone() *Record {
	c := *r
	c.Fields = make([]Field, len(r.Fields))
	copy(c.Fields, r.Fields)
	return &c
}

// recordFileName returns the file name used to store a record with the given id.
func recordFileName(id string) string { return id + ".xml" }

// encodeRecord renders a record to XML bytes, including the XML header.
func encodeRecord(r *Record) ([]byte, error) {
	body, err := xml.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	out := append([]byte(xml.Header), body...)
	return append(out, '\n'), nil
}

// decodeRecord parses record XML bytes.
func decodeRecord(data []byte) (*Record, error) {
	var r Record
	if err := xml.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("pod: malformed record XML: %w", err)
	}
	if r.ID == "" {
		return nil, errors.New("pod: record XML is missing the id attribute")
	}
	return &r, nil
}

// loadRecordFile reads and decodes a record file from disk.
func loadRecordFile(path string) (*Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decodeRecord(data)
}

// sortedFieldNames returns the union of field names collected from records,
// in first-seen order, then sorted. Used for derived schemas and SELECT *.
func sortedFieldNames(recs []*Record, limit int) []string {
	if limit <= 0 {
		limit = len(recs)
	}
	seen := map[string]struct{}{}
	var names []string
	for _, r := range recs {
		if r == nil {
			continue
		}
		for _, f := range r.Fields {
			if _, ok := seen[f.Name]; !ok {
				seen[f.Name] = struct{}{}
				names = append(names, f.Name)
			}
		}
	}
	sort.Strings(names)
	return names
}
