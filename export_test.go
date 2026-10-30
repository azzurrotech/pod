package pod

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
)

func exportRecords(t *testing.T) []*Record {
	t.Helper()
	recs := []*Record{
		{ID: "p1", Fields: []Field{
			{Name: "name", Value: "Alpha, Inc."},
			{Name: "status", Value: "active"},
			{Name: "note", Value: "line one\nline two"},
		}},
		{ID: "p2", Fields: []Field{
			{Name: "name", Value: "Beta"},
			{Name: "status", Value: "paused"},
		}},
	}
	return recs
}

func TestExportRowsDefaultsToIDFirst(t *testing.T) {
	header, rows := ExportRows(exportRecords(t), nil)
	if len(header) != 4 {
		t.Fatalf("header = %v, want 4 columns", header)
	}
	if header[0] != "id" {
		t.Errorf("first column = %q, want id", header[0])
	}
	if len(rows) != 2 || rows[0][0] != "p1" || rows[1][0] != "p2" {
		t.Errorf("rows = %v", rows)
	}
}

func TestExportRecordsCSVQuotesAndBOM(t *testing.T) {
	var buf bytes.Buffer
	if err := ExportRecords(&buf, exportRecords(t), ExportOptions{Format: FormatCSV}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "\ufeff") {
		t.Error("CSV export is missing the UTF-8 BOM")
	}
	parsed, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(out, "\ufeff"))).ReadAll()
	if err != nil {
		t.Fatalf("CSV does not parse: %v", err)
	}
	if len(parsed) != 3 {
		t.Fatalf("got %d CSV lines, want header + 2 rows", len(parsed))
	}
	if parsed[0][0] != "id" || parsed[0][1] != "name" {
		t.Errorf("header = %v", parsed[0])
	}
	if parsed[1][1] != "Alpha, Inc." {
		t.Errorf("comma-bearing value was not preserved: %q", parsed[1][1])
	}
	if parsed[1][3] != "line one\nline two" {
		t.Errorf("newline-bearing value was not preserved: %q", parsed[1][3])
	}
}

func TestExportRecordsCSVNoBOM(t *testing.T) {
	var buf bytes.Buffer
	if err := ExportRecords(&buf, exportRecords(t), ExportOptions{Format: FormatCSV, NoBOM: true}); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(buf.String(), "\ufeff") {
		t.Error("NoBOM was ignored")
	}
}

func TestExportRecordsColumnProjection(t *testing.T) {
	var buf bytes.Buffer
	err := ExportRecords(&buf, exportRecords(t), ExportOptions{
		Format: FormatJSON,
		Columns: []ExportColumn{
			{Field: "name", Header: "Project"},
			{Field: "status"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]string
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("JSON does not parse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d objects", len(got))
	}
	if got[0]["Project"] != "Alpha, Inc." || got[0]["status"] != "active" {
		t.Errorf("first row = %v", got[0])
	}
	if _, ok := got[0]["note"]; ok {
		t.Error("unselected column leaked into the export")
	}
	if _, ok := got[0]["name"]; ok {
		t.Error("header override was ignored: field key was exported")
	}
}

func TestExportCallerExportRows(t *testing.T) {
	rows := []map[string]string{
		{"title": "One", "source": "A"},
		{"title": "Two", "source": "B"},
	}
	var buf bytes.Buffer
	if err := Export(&buf, rows, ExportOptions{Format: FormatCSV, NoBOM: true}); err != nil {
		t.Fatal(err)
	}
	got, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d lines", len(got))
	}
	if got[0][0] != "source" || got[0][1] != "title" {
		t.Errorf("default order should be sorted: %v", got[0])
	}
	if got[2][1] != "Two" {
		t.Errorf("row = %v", got[2])
	}
}

func TestExportTableEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := ExportTable(&buf, []string{"id"}, nil, ExportOptions{Format: FormatJSON}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(buf.String()) != "[]" {
		t.Errorf("empty JSON export = %q", buf.String())
	}
}

func TestExportUnknownFormat(t *testing.T) {
	var buf bytes.Buffer
	if err := ExportRecords(&buf, exportRecords(t), ExportOptions{Format: "xml"}); err == nil {
		t.Fatal("unknown format was accepted")
	}
}

func TestExportFilename(t *testing.T) {
	cases := map[string]string{
		"stenella items": "stenella-items.csv",
		"":               "export.json",
		"a/b/../c":       "a-b-c.csv",
	}
	for base, want := range cases {
		format := FormatCSV
		if strings.HasSuffix(want, ".json") {
			format = FormatJSON
		}
		if got := ExportFilename(base, format); got != want {
			t.Errorf("ExportFilename(%q, %q) = %q, want %q", base, format, got, want)
		}
	}
}

func TestStructRows(t *testing.T) {
	type project struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	rows, err := StructRows([]*project{{ID: "1", Name: "Alpha"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["name"] != "Alpha" {
		t.Errorf("rows = %v", rows)
	}
}
