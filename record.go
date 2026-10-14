package pod

import (
	"encoding/xml"
	"errors"
	"fmt"
)

// timeFormat is the canonical timestamp layout used for record metadata.
const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"

// Field is a single key/value pair inside a record.
//
// A plain text control only uses Name and Value. Uploaded files additionally
// carry the metadata attributes below (Kind="file", Filename, ContentType,
// Size, SHA256) and encryption at rest tags a value with Enc="aes-256-gcm".
// The extra attributes are omitted from XML when empty, so ordinary records
// stay byte-for-byte as before.
type Field struct {
	Name  string `xml:"name,attr"`
	Value string `xml:",chardata"`

	// Kind is empty for an ordinary field or "file" for an uploaded blob
	// reference. It is advisory metadata and never changes query semantics.
	Kind string `xml:"kind,attr,omitempty"`
	// Filename is the original, client-supplied file name (display only; the
	// stored name is always sanitised and server-generated).
	Filename string `xml:"filename,attr,omitempty"`
	// ContentType is the client-supplied MIME type of an uploaded file.
	ContentType string `xml:"content_type,attr,omitempty"`
	// Size is the byte length of the stored blob (ciphertext length when
	// encryption at rest is enabled).
	Size int64 `xml:"size,attr,omitempty"`
	// SHA256 is the lowercase hex digest of the stored bytes.
	SHA256 string `xml:"sha256,attr,omitempty"`
	// Enc names the at-rest cipher that protects Value, or "" for plaintext.
	// Reads decrypt transparently; plaintext and ciphertext may coexist in one
	// table.
	Enc string `xml:"enc,attr,omitempty"`
}

// IsFile reports whether the field holds a file-upload reference.
func (f Field) IsFile() bool { return f.Kind == "file" }

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

// SetField inserts or replaces a whole field, matching on the field name and
// preserving every metadata attribute (Kind, Filename, Enc, …). It reports
// whether the stored field changed. This is how file uploads and encryption
// attach metadata that plain Set would drop.
func (r *Record) SetField(f Field) bool {
	for i := range r.Fields {
		if r.Fields[i].Name == f.Name {
			if r.Fields[i] == f {
				return false
			}
			r.Fields[i] = f
			return true
		}
	}
	r.Fields = append(r.Fields, f)
	return true
}

// Field returns the full field definition with the given name, if present.
func (r *Record) Field(name string) (Field, bool) {
	for i := range r.Fields {
		if r.Fields[i].Name == name {
			return r.Fields[i], true
		}
	}
	return Field{}, false
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
