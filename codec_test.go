package pod

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type codecSample struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Body    string    `json:"body,omitempty"`
	Count   int       `json:"count,omitempty"`
	Private bool      `json:"private,omitempty"`
	Tags    []string  `json:"tags,omitempty"`
	At      time.Time `json:"at,omitempty"`
}

func TestStructFieldsRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 29, 12, 30, 0, 0, time.UTC)
	in := codecSample{ID: "item-1", Title: "T", Count: 2, Tags: []string{"a", "b"}, At: at}
	m, err := StructFields(in)
	if err != nil {
		t.Fatal(err)
	}
	if m["id"] != "item-1" || m["title"] != "T" || m["count"] != "2" {
		t.Errorf("unexpected fields: %#v", m)
	}
	if _, ok := m["body"]; ok {
		t.Error("omitempty string was written")
	}
	if _, ok := m["private"]; ok {
		t.Error("omitempty bool was written")
	}
	if m["tags"] != `["a","b"]` {
		t.Errorf("tags = %q", m["tags"])
	}

	var out codecSample
	if err := ApplyStruct(m, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip mismatch:\n in=%#v\nout=%#v", in, out)
	}
}

func TestApplyStructIgnoresUnknownAndMissingFields(t *testing.T) {
	var out codecSample
	if err := ApplyStruct(map[string]string{"id": "x", "future": "ignored"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != "x" || out.Title != "" {
		t.Errorf("unexpected decode: %#v", out)
	}
}

func TestStructColumnsAndSchema(t *testing.T) {
	cols, err := StructColumns(codecSample{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"id", "title", "body", "count", "private", "tags", "at"}
	if !reflect.DeepEqual(cols, want) {
		t.Errorf("columns = %v, want %v", cols, want)
	}
	sch, err := StructSchema("t", codecSample{})
	if err != nil {
		t.Fatal(err)
	}
	if sch.Table != "t" || sch.Version != 1 || len(sch.Columns) != len(want) {
		t.Errorf("schema = %#v", sch)
	}
}

func TestUpsertAndGetStruct(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "pod"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureStructSchema("items", codecSample{}); err != nil {
		t.Fatal(err)
	}
	rec := codecSample{ID: "i1", Title: "one"}
	if _, _, err := st.UpsertStruct("items", rec.ID, rec); err != nil {
		t.Fatal(err)
	}
	var got codecSample
	if _, err := st.GetStruct("items", "i1", &got); err != nil {
		t.Fatal(err)
	}
	if got.Title != "one" {
		t.Errorf("title = %q", got.Title)
	}
	list, err := QueryStruct[codecSample](st, "items", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "i1" {
		t.Errorf("query = %#v", list)
	}
}

func TestRelationIndexBothDirections(t *testing.T) {
	ix := NewRelationIndex([]Relation{
		{From: "a", To: "b", Relation: "supports"},
		{From: "c", To: "a", Relation: "references"},
		{From: "a", To: "a", Relation: "related"},
		{From: "", To: "a"},
	})
	both := ix.Both("a")
	if len(both) != 3 {
		t.Fatalf("Both(a) = %#v", both)
	}
	if got := ix.Outbound("a"); len(got) != 2 {
		t.Errorf("Outbound(a) = %#v", got)
	}
	if got := ix.Inbound("a"); len(got) != 2 {
		t.Errorf("Inbound(a) = %#v", got)
	}
	if got := ix.Other(both[0], "a"); got != "c" {
		t.Errorf("Other(incoming) = %q", got)
	}
	if got := ix.Other(Relation{From: "a", To: "b"}, "a"); got != "b" {
		t.Errorf("Other(outgoing) = %q", got)
	}
}
