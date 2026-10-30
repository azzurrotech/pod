package pod

// This file holds the export helpers every pod-backed application needs when a
// record set leaves the platform: a stable column projection, a JSON document
// and a CSV document (with the byte-order mark spreadsheets expect), and the
// content-type/filename conventions that go with them.
//
// The helpers deliberately operate on the record *values* (field → string), not
// on an application's model, so the same code exports a project catalogue, an
// analysis log or an OSINT feed. Applications that already build a projection
// of their own can call Export with the rows they have.

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Export formats understood by Export and ExportRecords.
const (
	FormatJSON = "json"
	FormatCSV  = "csv"
)

// Column names one exported field and the header it carries. Field is the pod
// record field (or the key in a caller-built row); Header defaults to Field.
type ExportColumn struct {
	Field  string
	Header string
}

// ExportOptions controls the shape of an export.
type ExportOptions struct {
	// Format is FormatJSON (default) or FormatCSV.
	Format string
	// Columns selects and orders the exported fields. When empty, every field
	// present in the records is exported, in first-seen order with id first.
	Columns []ExportColumn
	// NoBOM suppresses the UTF-8 byte-order mark CSV exports carry by default
	// so spreadsheet applications open non-ASCII text correctly.
	NoBOM bool
}

// normalize returns the effective format, rejecting an unknown one.
func (o ExportOptions) normalize() (string, error) {
	f := strings.ToLower(strings.TrimSpace(o.Format))
	switch f {
	case "":
		return FormatJSON, nil
	case FormatJSON, FormatCSV:
		return f, nil
	default:
		return "", fmt.Errorf("pod: unknown export format %q (want json or csv)", o.Format)
	}
}

// ContentType is the media type for an export format.
func ContentType(format string) string {
	if strings.EqualFold(strings.TrimSpace(format), FormatCSV) {
		return "text/csv; charset=utf-8"
	}
	return "application/json; charset=utf-8"
}

// Extension is the file extension (without the dot) for an export format.
func Extension(format string) string {
	if strings.EqualFold(strings.TrimSpace(format), FormatCSV) {
		return "csv"
	}
	return "json"
}

// ExportFilename builds a safe download name for an export, e.g.
// ExportFilename("stenella-items", "csv") == "stenella-items.csv".
func ExportFilename(base, format string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "export"
	}
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
				b.WriteByte('-')
			}
		}
	}
	name := strings.Trim(b.String(), "-.")
	if name == "" {
		name = "export"
	}
	return name + "." + Extension(format)
}

// Rows projects records into a header and row set. With no columns, the union
// of the records' fields is used, id first, the rest in first-seen order.
func ExportRows(records []*Record, columns []ExportColumn) (header []string, rows [][]string) {
	cols := columns
	if len(cols) == 0 {
		cols = columnsOf(records)
	}
	header = make([]string, 0, len(cols))
	for _, c := range cols {
		h := c.Field
		if strings.TrimSpace(c.Header) != "" {
			h = c.Header
		}
		header = append(header, h)
	}
	rows = make([][]string, 0, len(records))
	for _, rec := range records {
		if rec == nil {
			continue
		}
		row := make([]string, len(cols))
		for i, c := range cols {
			if v, ok := rec.Get(c.Field); ok && v != "" {
				row[i] = v
				continue
			}
			// A record's id lives in an XML attribute, not in Fields, so the
			// conventional "id" column reads it from there.
			if c.Field == "id" {
				row[i] = rec.ID
			}
		}
		rows = append(rows, row)
	}
	return header, rows
}

// columnsOf derives the exported column set from the records themselves. A
// record's identity is an XML attribute rather than a field, so id always
// leads when there is at least one record.
func columnsOf(records []*Record) []ExportColumn {
	seen := map[string]bool{}
	var names []string
	any := false
	for _, rec := range records {
		if rec == nil {
			continue
		}
		any = true
		for _, f := range rec.Fields {
			if f.Name == "" || seen[f.Name] {
				continue
			}
			seen[f.Name] = true
			names = append(names, f.Name)
		}
	}
	if !any {
		return nil
	}
	out := []ExportColumn{{Field: "id"}}
	for _, n := range names {
		if n != "id" {
			out = append(out, ExportColumn{Field: n})
		}
	}
	return out
}

// ExportRecords writes records in the requested format.
func ExportRecords(w io.Writer, records []*Record, opts ExportOptions) error {
	header, rows := ExportRows(records, opts.Columns)
	return ExportTable(w, header, rows, opts)
}

// Export writes caller-built rows (field → value) in the requested format. The
// rows are projected onto opts.Columns; with no columns, the union of the row
// keys is used, id first.
func Export(w io.Writer, rows []map[string]string, opts ExportOptions) error {
	if w == nil {
		return fmt.Errorf("pod: nil export writer")
	}
	cols := opts.Columns
	if len(cols) == 0 {
		cols = columnsOfRows(rows)
	}
	header := make([]string, 0, len(cols))
	out := make([][]string, 0, len(rows))
	for _, c := range cols {
		h := c.Field
		if strings.TrimSpace(c.Header) != "" {
			h = c.Header
		}
		header = append(header, h)
	}
	for _, row := range rows {
		vals := make([]string, len(cols))
		for i, c := range cols {
			vals[i] = row[c.Field]
		}
		out = append(out, vals)
	}
	return ExportTable(w, header, out, opts)
}

// ExportTable writes an already-projected table. It is the last mile for an
// application that has its own row shape (stenella's sanitised item
// projection, for example).
func ExportTable(w io.Writer, header []string, rows [][]string, opts ExportOptions) error {
	if w == nil {
		return fmt.Errorf("pod: nil export writer")
	}
	format, err := opts.normalize()
	if err != nil {
		return err
	}
	if format == FormatCSV {
		return exportCSV(w, header, rows, opts.NoBOM)
	}
	return exportJSON(w, header, rows)
}

// exportCSV writes a BOM + header + rows. encoding/csv quotes any field that
// needs it, so commas, quotes and newlines in analyst text survive the round
// trip.
func exportCSV(w io.Writer, header []string, rows [][]string, noBOM bool) error {
	if !noBOM {
		if _, err := io.WriteString(w, "\ufeff"); err != nil {
			return err
		}
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(header); err != nil {
		return err
	}
	for _, row := range rows {
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// exportJSON writes an array of objects keyed by the header, preserving the
// header order in the object keys as far as encoding/json allows (it sorts map
// keys, which keeps the document stable).
func exportJSON(w io.Writer, header []string, rows [][]string) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	out := make([]map[string]string, 0, len(rows))
	for _, row := range rows {
		obj := make(map[string]string, len(header))
		for i, key := range header {
			obj[key] = row[i]
		}
		out = append(out, obj)
	}
	return enc.Encode(out)
}

// columnsOfRows derives the column set from caller-built rows.
func columnsOfRows(rows []map[string]string) []ExportColumn {
	seen := map[string]bool{}
	var names []string
	for _, row := range rows {
		for k := range row {
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			names = append(names, k)
		}
	}
	sort.Strings(names)
	if seen["id"] {
		out := []ExportColumn{{Field: "id"}}
		for _, n := range names {
			if n != "id" {
				out = append(out, ExportColumn{Field: n})
			}
		}
		return out
	}
	out := make([]ExportColumn, 0, len(names))
	for _, n := range names {
		out = append(out, ExportColumn{Field: n})
	}
	return out
}

// StructRows renders any slice of structs as exported rows using pod's own
// struct codec, so a caller that has already decoded records can export them
// without a second query.
func StructRows[T any](recs []*T) ([]map[string]string, error) {
	rows := make([]map[string]string, 0, len(recs))
	for _, rec := range recs {
		if rec == nil {
			continue
		}
		fields, err := StructFields(rec)
		if err != nil {
			return nil, err
		}
		rows = append(rows, fields)
	}
	return rows, nil
}
