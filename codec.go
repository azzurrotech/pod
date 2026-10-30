package pod

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// This file is pod's generic struct codec.
//
// pod's value type is map[string]string because a record is a set of named text
// fields. Applications almost always model a record as a Go struct, and every
// one of them used to repeat the same reflection pass. The functions here keep
// the mapping in pod itself:
//
//   - each exported field's JSON name becomes the pod field name;
//   - a `,omitempty` field is left out of the map when it is its zero value;
//   - strings, bools, numbers and time.Time map to their natural text form;
//   - slices, maps and nested structs are stored as compact JSON.
//
// The pod XML therefore stays readable, and adding a field to a record needs no
// migration code: pod's schema is a hint, not a gate.

var structTimeType = reflect.TypeOf(time.Time{})

// fieldTag returns the field's pod/JSON name and whether it is omitempty.
func fieldTag(f reflect.StructField) (name string, omit bool) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "-", false
	}
	name = f.Name
	if tag == "" {
		return name, false
	}
	parts := strings.Split(tag, ",")
	if parts[0] != "" {
		name = parts[0]
	}
	for _, p := range parts[1:] {
		if p == "omitempty" {
			omit = true
		}
	}
	return name, omit
}

// StructFields flattens a record struct into pod's field map.
func StructFields(v any) (map[string]string, error) {
	rv, err := structValue(v)
	if err != nil {
		return nil, err
	}
	rt := rv.Type()
	out := make(map[string]string, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		name, omit := fieldTag(f)
		if name == "-" {
			continue
		}
		s, skip, err := encodeStructValue(rv.Field(i), omit)
		if err != nil {
			return nil, fmt.Errorf("pod codec: %s.%s: %w", rt.Name(), f.Name, err)
		}
		if skip {
			continue
		}
		out[name] = s
	}
	return out, nil
}

// ApplyStruct rebuilds a record struct from pod's field map. Fields absent from
// the map keep their zero value, so an older record stays readable.
func ApplyStruct(m map[string]string, v any) error {
	rv, err := structValue(v)
	if err != nil {
		return err
	}
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" {
			continue
		}
		name, _ := fieldTag(f)
		if name == "-" {
			continue
		}
		s, ok := m[name]
		if !ok {
			continue
		}
		if err := decodeStructValue(s, rv.Field(i)); err != nil {
			return fmt.Errorf("pod codec: %s.%s: %w", rt.Name(), f.Name, err)
		}
	}
	return nil
}

// StructColumns derives the pod column names from a record struct, so a
// table's declared schema always matches the type that fills it.
func StructColumns(sample any) ([]string, error) {
	rv, err := structValue(sample)
	if err != nil {
		return nil, err
	}
	rt := rv.Type()
	names := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" {
			continue
		}
		name, _ := fieldTag(f)
		if name == "-" {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}

// StructSchema returns the column definition for a record struct.
func StructSchema(table string, sample any) (*Schema, error) {
	names, err := StructColumns(sample)
	if err != nil {
		return nil, err
	}
	cols := make([]Column, 0, len(names))
	for _, n := range names {
		cols = append(cols, Column{Name: n, Type: "text"})
	}
	return NewSchema(table, cols), nil
}

// EnsureStructSchema creates or widens a table so its columns match the record
// struct. It is safe to call on every start: UpdateSchema only adds columns.
func (s *Store) EnsureStructSchema(table string, sample any) error {
	names, err := StructColumns(sample)
	if err != nil {
		return err
	}
	cols := make([]Column, 0, len(names))
	for _, n := range names {
		cols = append(cols, Column{Name: n, Type: "text"})
	}
	if err := s.UpdateSchema(table, cols); err != nil {
		return fmt.Errorf("pod: schema %s: %w", table, err)
	}
	return nil
}

// UpsertStruct writes a record struct to a table.
func (s *Store) UpsertStruct(table, id string, rec any) (*Record, bool, error) {
	fields, err := StructFields(rec)
	if err != nil {
		return nil, false, err
	}
	r, created, err := s.Upsert(table, id, fields)
	if err != nil {
		return nil, false, fmt.Errorf("pod: upsert %s/%s: %w", table, id, err)
	}
	return r, created, nil
}

// GetStruct reads one record and decodes it into out (a pointer to a struct).
func (s *Store) GetStruct(table, id string, out any) (*Record, error) {
	r, err := s.Get(table, id)
	if err != nil {
		return nil, err
	}
	if err := ApplyStruct(r.Values(), out); err != nil {
		return nil, err
	}
	return r, nil
}

// QueryStruct decodes every matching record into a new slice of T. Records
// written by an older schema that no longer decode are skipped rather than
// failing the query, which is what keeps a growing model readable.
func QueryStruct[T any](s *Store, table string, cond Cond, opts *QueryOptions) ([]*T, error) {
	recs, err := s.Query(table, cond, opts)
	if err != nil {
		return nil, err
	}
	out := make([]*T, 0, len(recs))
	for _, r := range recs {
		var v T
		if err := ApplyStruct(r.Values(), &v); err != nil {
			continue
		}
		out = append(out, &v)
	}
	return out, nil
}

func structValue(v any) (reflect.Value, error) {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return reflect.Value{}, fmt.Errorf("pod codec: nil %T", v)
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("pod codec: %T is not a struct", v)
	}
	return rv, nil
}

// encodeStructValue renders one field as text. skip reports that an omitempty
// field was empty and should not be written.
func encodeStructValue(fv reflect.Value, omit bool) (s string, skip bool, err error) {
	if !fv.IsValid() {
		return "", true, nil
	}
	if fv.Kind() == reflect.Ptr {
		if fv.IsNil() {
			return "", true, nil
		}
		fv = fv.Elem()
	}
	if fv.Type() == structTimeType {
		t := fv.Interface().(time.Time)
		if t.IsZero() {
			return "", omit, nil
		}
		return t.UTC().Format(time.RFC3339), false, nil
	}
	switch fv.Kind() {
	case reflect.String:
		s = fv.String()
		return s, omit && s == "", nil
	case reflect.Bool:
		b := fv.Bool()
		if omit && !b {
			return "", true, nil
		}
		return strconv.FormatBool(b), false, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n := fv.Int()
		if omit && n == 0 {
			return "", true, nil
		}
		return strconv.FormatInt(n, 10), false, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n := fv.Uint()
		if omit && n == 0 {
			return "", true, nil
		}
		return strconv.FormatUint(n, 10), false, nil
	case reflect.Float32, reflect.Float64:
		f := fv.Float()
		if omit && f == 0 {
			return "", true, nil
		}
		return strconv.FormatFloat(f, 'g', -1, 64), false, nil
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Struct:
		if omit && fv.Len() == 0 {
			return "", true, nil
		}
		b, err := json.Marshal(fv.Interface())
		if err != nil {
			return "", false, err
		}
		return string(b), false, nil
	}
	return "", false, fmt.Errorf("unsupported field kind %s", fv.Kind())
}

// decodeStructValue parses one field's text back into the struct field.
func decodeStructValue(s string, fv reflect.Value) error {
	if fv.Type() == structTimeType {
		t, err := ParseTime(s)
		if err != nil {
			return err
		}
		fv.Set(reflect.ValueOf(t))
		return nil
	}
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(s)
	case reflect.Bool:
		b, err := strconv.ParseBool(strings.TrimSpace(s))
		if err != nil {
			return err
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return err
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return err
		}
		fv.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return err
		}
		fv.SetFloat(f)
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Struct:
		return json.Unmarshal([]byte(s), fv.Addr().Interface())
	default:
		return fmt.Errorf("unsupported field kind %s", fv.Kind())
	}
	return nil
}

// ParseTime accepts the RFC 3339 form the codec writes, and degrades to the
// plain textual forms pod's HTML forms submit.
func ParseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	for _, l := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("pod codec: cannot parse time %q", s)
}
