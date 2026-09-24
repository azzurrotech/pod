package pod

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestHandler(t *testing.T, mount string) (*Handler, *Store) {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(HandlerOptions{Store: st, Mount: mount}), st
}

func boolPtr(b bool) *bool { return &b }

func serve(h *Handler, method, target, ctype string, body io.Reader) *httptest.ResponseRecorder {
	t := httptest.NewRequest(method, target, body)
	if ctype != "" {
		t.Header.Set("Content-Type", ctype)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, t)
	return rr
}

func formBody(vals url.Values) io.Reader { return strings.NewReader(vals.Encode()) }

func mustUpsert(t *testing.T, st *Store, table, id string, vals map[string]string) {
	t.Helper()
	if _, _, err := st.Upsert(table, id, vals); err != nil {
		t.Fatal(err)
	}
}

func TestHandlerHealth(t *testing.T) {
	h, _ := newTestHandler(t, "/")
	rr := serve(h, "GET", "/health", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", rr.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Fatalf("health status body = %v", body["status"])
	}
	if body["service"] != "pod" {
		t.Fatalf("health service = %v", body["service"])
	}
}

func TestHandlerCreateTableForm(t *testing.T) {
	h, st := newTestHandler(t, "/")
	vals := url.Values{}
	vals.Set("_table", "people")
	vals.Set("_cname_0", "name")
	vals.Set("_ctype_0", "text")
	vals.Set("_cdefault_0", "anon")
	rr := serve(h, "POST", "/tables", "application/x-www-form-urlencoded", formBody(vals))
	if rr.Code != http.StatusOK {
		t.Fatalf("create table status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(st.base, "people", ".pod-schema.xml")); err != nil {
		t.Fatalf("schema file not written: %v", err)
	}
	// schema endpoint reflects the columns
	rr = serve(h, "GET", "/schema/people?format=json", "", nil)
	var sc TableSchema
	if err := json.Unmarshal(rr.Body.Bytes(), &sc); err != nil {
		t.Fatal(err)
	}
	if len(sc.Columns) != 1 || sc.Columns[0].Name != "name" {
		t.Fatalf("schema = %+v", sc)
	}
	if sc.Columns[0].Default == nil || *sc.Columns[0].Default != "anon" {
		t.Fatalf("column default = %v, want anon", sc.Columns[0].Default)
	}
}

func TestHandlerCreateTableJSON(t *testing.T) {
	h, _ := newTestHandler(t, "/")
	body := `{"table":"acme/contacts","columns":[{"name":"email","type":"text"},{"name":"company","type":"text","default":"ACME"}]}`
	rr := serve(h, "POST", "/tables", "application/json", strings.NewReader(body))
	if rr.Code != http.StatusOK {
		t.Fatalf("create table status = %d, body=%s", rr.Code, rr.Body.String())
	}
	rr = serve(h, "GET", "/tables?format=json", "", nil)
	var out jsonTables
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Tables) != 1 || out.Tables[0].Name != "acme/contacts" {
		t.Fatalf("tables = %+v", out.Tables)
	}
	if out.Tables[0].Schema == nil || len(out.Tables[0].Schema.Columns) != 2 {
		t.Fatalf("schema cols = %+v", out.Tables[0].Schema)
	}
}

func TestHandlerFormUpsertRedirect(t *testing.T) {
	h, st := newTestHandler(t, "/")
	vals := url.Values{}
	vals.Set("name", "jane")
	vals.Set("age", "30")
	rr := serve(h, "POST", "/table/people?format=html", "application/x-www-form-urlencoded", formBody(vals))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("upsert redirect status = %d, want 303, body=%s", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); !strings.HasPrefix(loc, "/table/people?notice=") {
		t.Fatalf("Location = %q", loc)
	}
	// the record landed at the URL-destination path on disk
	entries, err := os.ReadDir(filepath.Join(st.base, "people"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".xml") && !strings.HasPrefix(e.Name(), ".") {
			found = true
		}
	}
	if !found {
		t.Fatal("no record XML file written under the table directory")
	}
}

func TestHandlerTableQueryJSONAndXML(t *testing.T) {
	h, st := newTestHandler(t, "/")
	mustUpsert(t, st, "people", "1", map[string]string{"name": "jane", "age": "30"})

	rr := serve(h, "GET", "/table/people?format=json", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("json query status = %d", rr.Code)
	}
	var list jsonRecordList
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Count != 1 || len(list.Records) != 1 {
		t.Fatalf("json query = %+v", list)
	}
	if list.Records[0].Fields["name"] != "jane" {
		t.Fatalf("record fields = %v", list.Records[0].Fields)
	}

	rr = serve(h, "GET", "/table/people?format=xml", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("xml query status = %d", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/xml") {
		t.Fatalf("xml content-type = %q", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "<records") || !strings.Contains(body, `id="1"`) {
		t.Fatalf("xml body missing records: %s", body)
	}
}

func TestHandlerTableFilterAndFreeText(t *testing.T) {
	h, st := newTestHandler(t, "/")
	mustUpsert(t, st, "people", "1", map[string]string{"name": "jane", "city": "toronto"})
	mustUpsert(t, st, "people", "2", map[string]string{"name": "bob", "city": "milan"})

	count := func(target string) int {
		t.Helper()
		rr := serve(h, "GET", target, "", nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status = %d", target, rr.Code)
		}
		var list jsonRecordList
		if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		return list.Count
	}
	if n := count("/table/people?name=jane&format=json"); n != 1 {
		t.Fatalf("equality filter count = %d, want 1", n)
	}
	if n := count("/table/people?name=bob&format=json"); n != 1 {
		t.Fatalf("equality filter count = %d, want 1", n)
	}
	if n := count("/table/people?name=nobody&format=json"); n != 0 {
		t.Fatalf("no-match filter count = %d, want 0", n)
	}
	if n := count("/table/people?q=toronto&format=json"); n != 1 {
		t.Fatalf("free-text count = %d, want 1", n)
	}
	// limit
	rr := serve(h, "GET", "/table/people?format=json&limit=1", "", nil)
	var list jsonRecordList
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Count != 1 {
		t.Fatalf("limit count = %d, want 1", list.Count)
	}
}

func TestHandlerRecordEndpoints(t *testing.T) {
	h, st := newTestHandler(t, "/")
	mustUpsert(t, st, "people", "1", map[string]string{"name": "jane"})

	// HTML detail page (exercises the embedded template)
	rr := serve(h, "GET", "/record/people/1?format=html", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("record html status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `people</a> / 1`) {
		t.Fatalf("record html missing heading: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "jane") {
		t.Fatalf("record html missing value: %s", rr.Body.String())
	}

	// JSON view
	rr = serve(h, "GET", "/record/people/1?format=json", "", nil)
	var rec jsonRecord
	if err := json.Unmarshal(rr.Body.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.ID != "1" || rec.Fields["name"] != "jane" {
		t.Fatalf("record json = %+v", rec)
	}

	// XML view
	rr = serve(h, "GET", "/record/people/1?format=xml", "", nil)
	if !strings.Contains(rr.Body.String(), "<record") {
		t.Fatalf("record xml = %s", rr.Body.String())
	}

	// update via form POST (PRG)
	vals := url.Values{}
	vals.Set("name", "jane doe")
	rr = serve(h, "POST", "/record/people/1?format=html", "application/x-www-form-urlencoded", formBody(vals))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("record update status = %d", rr.Code)
	}
	got, err := st.Get("people", "1")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := got.Get("name"); v != "jane doe" {
		t.Fatalf("updated name = %q", v)
	}

	// delete via form POST
	vals = url.Values{}
	vals.Set("_action", "delete")
	rr = serve(h, "POST", "/record/people/1?format=html", "application/x-www-form-urlencoded", formBody(vals))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("record delete status = %d", rr.Code)
	}
	if _, err := st.Get("people", "1"); err != ErrNotFound {
		t.Fatalf("record after delete = %v, want ErrNotFound", err)
	}

	// DELETE method removes directly
	mustUpsert(t, st, "people", "2", map[string]string{"name": "bob"})
	rr = serve(h, "DELETE", "/record/people/2", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d", rr.Code)
	}
	if _, err := st.Get("people", "2"); err != ErrNotFound {
		t.Fatalf("record after DELETE = %v", err)
	}
}

func TestHandlerTableHTMLRenders(t *testing.T) {
	h, st := newTestHandler(t, "/")
	mustUpsert(t, st, "people", "1", map[string]string{"name": "jane"})
	rr := serve(h, "GET", "/table/people?format=html", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("table html status = %d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `name="name"`) || !strings.Contains(body, "Save a record") {
		t.Fatalf("table html missing form: %s", body)
	}
	if !strings.Contains(body, "jane") {
		t.Fatalf("table html missing record value: %s", body)
	}
}

func TestHandlerIndexHTML(t *testing.T) {
	h, st := newTestHandler(t, "/")
	mustUpsert(t, st, "people", "1", map[string]string{"name": "jane"})
	rr := serve(h, "GET", "/?format=html", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("index html status = %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "people") || !strings.Contains(body, "Create table") {
		t.Fatalf("index html: %s", body)
	}
}

func TestHandlerSQL(t *testing.T) {
	h, st := newTestHandler(t, "/")
	mustUpsert(t, st, "people", "1", map[string]string{"name": "jane", "age": "30"})

	rr := serve(h, "GET", "/sql?sql="+url.QueryEscape("SELECT * FROM people"), "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("sql get status = %d body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Columns []string        `json:"columns"`
		Rows    [][]interface{} `json:"rows"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 1 {
		t.Fatalf("sql rows = %d, want 1", len(out.Rows))
	}
	if out.Rows[0][0] != "1" {
		t.Fatalf("sql row = %v", out.Rows[0])
	}

	// POST JSON with args
	body := `{"sql":"SELECT name FROM people WHERE age = ?","args":["30"]}`
	rr = serve(h, "POST", "/sql", "application/json", strings.NewReader(body))
	if rr.Code != http.StatusOK {
		t.Fatalf("sql post status = %d body=%s", rr.Code, rr.Body.String())
	}
	var out2 struct {
		Columns []string        `json:"columns"`
		Rows    [][]interface{} `json:"rows"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out2); err != nil {
		t.Fatal(err)
	}
	if len(out2.Rows) != 1 || out2.Rows[0][0] != "jane" {
		t.Fatalf("sql post rows = %v", out2.Rows)
	}

	// bad SQL
	rr = serve(h, "GET", "/sql?sql="+url.QueryEscape("BOGUS"), "", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad sql status = %d, want 400", rr.Code)
	}
}

func TestHandlerNormalizeAndReindexEndpoints(t *testing.T) {
	h, st := newTestHandler(t, "/")
	if err := st.UpdateSchema("t", []Column{{Name: "a", Type: "text"}}); err != nil {
		t.Fatal(err)
	}
	mustUpsert(t, st, "t", "1", map[string]string{"a": "x"})
	if err := st.UpdateSchema("t", []Column{{Name: "a"}, {Name: "flag", Default: strPtr("off")}}); err != nil {
		t.Fatal(err)
	}

	rr := serve(h, "POST", "/normalize", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("normalize status = %d body=%s", rr.Code, rr.Body.String())
	}
	var nrm map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &nrm); err != nil {
		t.Fatal(err)
	}
	if nrm["normalized"].(float64) != 1 {
		t.Fatalf("normalized = %v, want 1", nrm["normalized"])
	}
	rec, _ := st.Get("t", "1")
	if v, _ := rec.Get("flag"); v != "off" {
		t.Fatalf("flag = %q, want off", v)
	}

	rr = serve(h, "POST", "/reindex", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("reindex status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandlerNotFoundAndMethodNotAllowed(t *testing.T) {
	h, _ := newTestHandler(t, "/")
	rr := serve(h, "GET", "/nope", "", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("not found status = %d, want 404", rr.Code)
	}
	rr = serve(h, "DELETE", "/tables", "", nil)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status = %d, want 405", rr.Code)
	}
	_ = rr.Body
}

func TestHandlerMountPrefix(t *testing.T) {
	h, _ := newTestHandler(t, "/pod")
	rr := serve(h, "GET", "/pod/health", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("mounted health status = %d", rr.Code)
	}
	rr = serve(h, "GET", "/health", "", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unmounted path status = %d, want 404", rr.Code)
	}
}

func TestHandlerFormatNegotiation(t *testing.T) {
	h, st := newTestHandler(t, "/")
	mustUpsert(t, st, "t", "1", map[string]string{"v": "x"})

	req := httptest.NewRequest("GET", "/table/t", nil)
	req.Header.Set("Accept", "application/xml")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if !strings.HasPrefix(rr.Header().Get("Content-Type"), "application/xml") {
		t.Fatalf("Accept xml content-type = %q", rr.Header().Get("Content-Type"))
	}

	req = httptest.NewRequest("GET", "/table/t", nil)
	req.Header.Set("Accept", "text/html")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if !strings.HasPrefix(rr.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("Accept html content-type = %q", rr.Header().Get("Content-Type"))
	}
}

func TestHandlerUIDisabledFallsBackToJSON(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(HandlerOptions{Store: st, Mount: "/", UI: boolPtr(false)})
	rr := serve(h, "GET", "/?format=html", "", nil)
	// ui disabled: render() writes JSON instead of a template
	if !strings.HasPrefix(rr.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("ui=false content-type = %q", rr.Header().Get("Content-Type"))
	}
}

func TestHandlerXMLBodyUpsert(t *testing.T) {
	h, st := newTestHandler(t, "/")
	body := `<?xml version="1.0"?><record id="9"><field name="name">grace</field></record>`
	rr := serve(h, "POST", "/table/people?format=json", "application/xml", strings.NewReader(body))
	if rr.Code != http.StatusOK {
		t.Fatalf("xml body upsert status = %d body=%s", rr.Code, rr.Body.String())
	}
	rec, err := st.Get("people", "9")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := rec.Get("name"); v != "grace" {
		t.Fatalf("name = %q", v)
	}
}

func TestHandlerJSONBodyUpsert(t *testing.T) {
	h, st := newTestHandler(t, "/")
	body := `{"id":"7","fields":{"name":"grace","age":"33"}}`
	rr := serve(h, "PUT", "/table/people", "application/json", strings.NewReader(body))
	if rr.Code != http.StatusOK {
		t.Fatalf("json body upsert status = %d body=%s", rr.Code, rr.Body.String())
	}
	rec, err := st.Get("people", "7")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := rec.Get("age"); v != "33" {
		t.Fatalf("age = %q", v)
	}
}
