package pod

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Version is the current pod version.
const Version = "1.0.0"

// HandlerOptions configures the HTTP handler (middleware) exposed by pod.
type HandlerOptions struct {
	Store *Store
	// Mount is the URL prefix the handler is served under, e.g. "/pod".
	// Use "/" for a standalone server. Defaults to "/pod".
	Mount string
	// UI enables the embedded HTML form interface. A nil value means enabled;
	// set it to a pointer to false to disable the UI.
	UI *bool
}

// Handler serves the pod database over HTTP. It accepts request formats that
// HTML forms produce (application/x-www-form-urlencoded and multipart), a
// JSON API, and XML bodies. It also serves the built-in HTML form UI, the
// database/sql console endpoint, and health/normalize/reindex endpoints.
//
// Routes (mounted at Mount):
//
//	GET  {mount}/                          index page / table list
//	GET  {mount}/health                    JSON health status
//	GET  {mount}/tables                    list tables (json/xml/html)
//	POST {mount}/tables                    create or update a table schema
//	GET  {mount}/table/<path>              query records (?field=value)
//	POST {mount}/table/<path>              upsert a record (HTML form action)
//	PUT  {mount}/table/<path>              upsert a record (API)
//	GET  {mount}/record/<path>/<id>        fetch one record
//	PUT  {mount}/record/<path>/<id>        update one record
//	POST {mount}/record/<path>/<id>        update or delete (_action=delete)
//	DELETE {mount}/record/<path>/<id>      delete one record
//	GET  {mount}/schema/<path>             fetch a table schema
//	GET  {mount}/sql?sql=...               run SQL (database/sql engine)
//	POST {mount}/sql                       run SQL (form field or JSON body)
//	POST {mount}/normalize                 run the normalizer now
//	POST {mount}/reindex                   run the indexer sync now
//
// The destination path of a form submission maps directly to the storage
// location: POST /pod/table/acme/contacts stores records under
// <base>/acme/contacts/<id>.xml.
type Handler struct {
	store      *Store
	mount      string
	ui         bool
	tpl        *template.Template
	indexer    *Indexer
	normalizer *Normalizer
	started    time.Time
}

// NewHandler builds a Handler for the given options.
func NewHandler(o HandlerOptions) *Handler {
	if o.Store == nil {
		panic("pod: NewHandler requires a Store")
	}
	mount := o.Mount
	if mount == "" {
		mount = "/pod"
	}
	if !strings.HasPrefix(mount, "/") {
		mount = "/" + mount
	}
	mount = strings.TrimSuffix(mount, "/")
	if mount == "" {
		mount = "/"
	}
	ui := true
	if o.UI != nil {
		ui = *o.UI
	}
	h := &Handler{
		store:      o.Store,
		mount:      mount,
		ui:         ui,
		indexer:    NewIndexer(o.Store),
		normalizer: NewNormalizer(o.Store),
		started:    time.Now(),
	}
	if h.ui {
		funcs := template.FuncMap{
			// field returns a record's field value (or "") for templates.
			"field": func(r *Record, name string) string {
				if r == nil {
					return ""
				}
				v, _ := r.Get(name)
				return v
			},
			// esc URL-escapes a single path segment for use in hrefs.
			"esc": url.PathEscape,
		}
		tpl, err := template.New("").Funcs(funcs).ParseFS(uiFS, "ui/*.html")
		if err != nil {
			panic(fmt.Sprintf("pod: failed to parse UI templates: %v", err))
		}
		h.tpl = tpl
	}
	return h
}

// Store returns the underlying store.
func (h *Handler) Store() *Store { return h.store }

// Mount returns the mount prefix of the handler.
func (h *Handler) Mount() string { return h.mount }

// Indexer returns the handler's indexer service.
func (h *Handler) Indexer() *Indexer { return h.indexer }

// Normalizer returns the handler's normalizer service.
func (h *Handler) Normalizer() *Normalizer { return h.normalizer }

// RunBackground starts the indexer and normalizer services.
func (h *Handler) RunBackground(ctx context.Context, indexInterval, normalizeInterval time.Duration) {
	if indexInterval > 0 {
		go h.indexer.Run(ctx, indexInterval)
	}
	if normalizeInterval > 0 {
		go h.normalizer.Run(ctx, normalizeInterval)
	}
}

// ServeHTTP dispatches requests to the pod routes.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Pod-Version", Version)
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	p, ok := h.stripMount(r.URL.Path)
	if !ok {
		h.writeError(w, r, http.StatusNotFound, "not found")
		return
	}
	switch {
	case p == "/" || p == "":
		if r.Method != http.MethodGet {
			h.methodNotAllowed(w, r)
			return
		}
		h.handleRoot(w, r)
	case p == "/health":
		if r.Method != http.MethodGet {
			h.methodNotAllowed(w, r)
			return
		}
		h.handleHealth(w, r)
	case p == "/tables":
		switch r.Method {
		case http.MethodGet:
			h.handleListTables(w, r)
		case http.MethodPost:
			h.handleCreateTable(w, r)
		default:
			h.methodNotAllowed(w, r)
		}
	case p == "/sql":
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			h.methodNotAllowed(w, r)
			return
		}
		h.handleSQL(w, r)
	case p == "/normalize":
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			h.methodNotAllowed(w, r)
			return
		}
		h.handleNormalize(w, r)
	case p == "/reindex":
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			h.methodNotAllowed(w, r)
			return
		}
		h.handleReindex(w, r)
	case strings.HasPrefix(p, "/table/"):
		rest := strings.TrimPrefix(p, "/table/")
		if rest == "" {
			h.writeError(w, r, http.StatusBadRequest, "missing table path")
			return
		}
		h.handleTable(w, r, rest)
	case strings.HasPrefix(p, "/record/"):
		segs := strings.Split(strings.TrimPrefix(p, "/record/"), "/")
		if len(segs) < 2 {
			h.writeError(w, r, http.StatusBadRequest, "expected /record/<table>/<id>")
			return
		}
		table := strings.Join(segs[:len(segs)-1], "/")
		id := segs[len(segs)-1]
		h.handleRecord(w, r, table, id)
	case strings.HasPrefix(p, "/schema/"):
		rest := strings.TrimPrefix(p, "/schema/")
		if rest == "" {
			h.writeError(w, r, http.StatusBadRequest, "missing table path")
			return
		}
		h.handleSchema(w, r, rest)
	default:
		h.writeError(w, r, http.StatusNotFound, "not found")
	}
}

func (h *Handler) stripMount(p string) (string, bool) {
	if h.mount == "" || h.mount == "/" {
		return p, true
	}
	if p == h.mount {
		return "/", true
	}
	if strings.HasPrefix(p, h.mount+"/") {
		return strings.TrimPrefix(p, h.mount), true
	}
	return "", false
}

func (h *Handler) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "GET, POST, PUT, DELETE")
	h.writeError(w, r, http.StatusMethodNotAllowed, "method not allowed")
}

// url joins the mount prefix with a route path.
func (h *Handler) url(p string) string {
	if h.mount == "/" {
		return p
	}
	return h.mount + p
}

// --- format negotiation -----------------------------------------------------

func (h *Handler) format(r *http.Request) string {
	if f := r.URL.Query().Get("format"); f != "" {
		switch strings.ToLower(f) {
		case "json", "xml", "html":
			return strings.ToLower(f)
		}
	}
	ct := r.Header.Get("Content-Type")
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		if strings.HasPrefix(ct, "application/json") {
			return "json"
		}
		if strings.HasPrefix(ct, "application/xml") || strings.HasPrefix(ct, "text/xml") {
			return "xml"
		}
	}
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") {
		return "json"
	}
	if strings.Contains(accept, "application/xml") || strings.Contains(accept, "text/xml") {
		return "xml"
	}
	if strings.Contains(accept, "text/html") {
		return "html"
	}
	return "json"
}

// --- response writers -------------------------------------------------------

func (h *Handler) writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (h *Handler) writeXML(w http.ResponseWriter, code int, v interface{}) {
	body, err := xml.MarshalIndent(v, "", "  ")
	if err != nil {
		h.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(code)
	w.Write([]byte(xml.Header))
	w.Write(body)
	w.Write([]byte("\n"))
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, code int, msg string) {
	switch h.format(r) {
	case "xml":
		msg = strings.ReplaceAll(msg, "\n", " ")
		h.writeXML(w, code, xmlError{Message: msg})
	case "html":
		if h.ui {
			h.render(w, r, "message.html", messageData{
				Mount:   h.mount,
				Title:   fmt.Sprintf("%d Error", code),
				Message: msg,
			})
			return
		}
		http.Error(w, msg, code)
	default:
		h.writeJSON(w, code, map[string]string{"error": msg})
	}
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, name string, data interface{}) {
	if !h.ui || h.tpl == nil {
		h.writeJSON(w, http.StatusOK, data)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// --- body parsing -----------------------------------------------------------

func (h *Handler) parseBody(r *http.Request) (map[string]string, error) {
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "multipart/form-data"):
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			return nil, err
		}
		return formValues(r.Form), nil
	case strings.HasPrefix(ct, "application/x-www-form-urlencoded"),
		ct == "" && r.Method == http.MethodPost:
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		return formValues(r.Form), nil
	case strings.HasPrefix(ct, "application/json"):
		dec := json.NewDecoder(r.Body)
		dec.UseNumber()
		var obj map[string]interface{}
		if err := dec.Decode(&obj); err != nil {
			return nil, fmt.Errorf("invalid JSON body: %v", err)
		}
		return flattenJSON(obj), nil
	case strings.HasPrefix(ct, "application/xml") || strings.HasPrefix(ct, "text/xml"):
		data, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		rec, err := decodeRecord(data)
		if err != nil {
			return nil, err
		}
		m := make(map[string]string, len(rec.Fields)+3)
		m["id"] = rec.ID
		for _, f := range rec.Fields {
			m[f.Name] = f.Value
		}
		return m, nil
	}
	return nil, fmt.Errorf("unsupported content type %q", ct)
}

func formValues(f url.Values) map[string]string {
	m := make(map[string]string, len(f))
	for k, vs := range f {
		if len(vs) == 0 {
			continue
		}
		m[k] = strings.Join(vs, "\n")
	}
	return m
}

func flattenJSON(obj map[string]interface{}) map[string]string {
	m := make(map[string]string, len(obj))
	for k, v := range obj {
		if k == "fields" {
			// accept a nested "fields" object (the response shape) as well as
			// top-level flat fields, keeping the API symmetric
			if fm, ok := v.(map[string]interface{}); ok {
				for fk, fv := range fm {
					m[fk] = scalarString(fv)
				}
				continue
			}
		}
		m[k] = scalarString(v)
	}
	return m
}

func scalarString(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case int64:
		return strconv.FormatInt(t, 10)
	default:
		if b, err := json.Marshal(t); err == nil {
			return string(b)
		}
		return ""
	}
}

// --- record JSON / XML shapes -----------------------------------------------

type jsonRecord struct {
	ID            string            `json:"id"`
	Created       string            `json:"created,omitempty"`
	Updated       string            `json:"updated,omitempty"`
	Version       int               `json:"version"`
	SchemaVersion int               `json:"schema_version"`
	Fields        map[string]string `json:"fields"`
}

func recordJSON(r *Record) jsonRecord {
	return jsonRecord{
		ID:            r.ID,
		Created:       r.Created,
		Updated:       r.Updated,
		Version:       r.Version,
		SchemaVersion: r.SchemaVersion,
		Fields:        r.Values(),
	}
}

type jsonRecordList struct {
	Count   int          `json:"count"`
	Records []jsonRecord `json:"records"`
}

type jsonTables struct {
	Tables []jsonTable `json:"tables"`
}

type jsonTable struct {
	Name   string       `json:"name"`
	Count  int          `json:"count"`
	Schema *TableSchema `json:"schema,omitempty"`
}

type TableSchema struct {
	Table   string       `json:"table"`
	Version int          `json:"version"`
	Columns []ColumnJSON `json:"columns"`
}

type ColumnJSON struct {
	Name    string  `json:"name"`
	Type    string  `json:"type,omitempty"`
	Default *string `json:"default,omitempty"`
}

func schemaJSON(sc *Schema) *TableSchema {
	if sc == nil {
		return nil
	}
	out := &TableSchema{Table: sc.Table, Version: sc.Version}
	out.Columns = make([]ColumnJSON, 0, len(sc.Columns))
	for _, c := range sc.Columns {
		out.Columns = append(out.Columns, ColumnJSON{Name: c.Name, Type: c.Type, Default: c.Default})
	}
	return out
}

type xmlError struct {
	XMLName xml.Name `xml:"error"`
	Message string   `xml:"message"`
}

type xmlRecords struct {
	XMLName xml.Name `xml:"records"`
	Table   string   `xml:"table,attr,omitempty"`
	Count   int      `xml:"count,attr"`
	Records []Record `xml:"record"`
}

type xmlTables struct {
	XMLName xml.Name       `xml:"tables"`
	Tables  []xmlTableInfo `xml:"table"`
}

type xmlTableInfo struct {
	Name    string         `xml:"name,attr"`
	Count   int            `xml:"count,attr"`
	Columns []xmlColumnDef `xml:"column,omitempty"`
}

type xmlColumnDef struct {
	Name    string  `xml:"name,attr"`
	Type    string  `xml:"type,attr,omitempty"`
	Default *string `xml:"default,attr,omitempty"`
}

type xmlUpsertResult struct {
	XMLName xml.Name `xml:"result"`
	Status  string   `xml:"status"`
	ID      string   `xml:"id"`
	Created bool     `xml:"created"`
}

type xmlMessage struct {
	XMLName xml.Name `xml:"result"`
	Status  string   `xml:"status"`
	Message string   `xml:"message,omitempty"`
}

// --- route handlers ---------------------------------------------------------

func (h *Handler) handleRoot(w http.ResponseWriter, r *http.Request) {
	switch h.format(r) {
	case "html":
		names, err := h.store.ListTables()
		if err != nil {
			h.writeError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		data := indexData{Mount: h.mount}
		for _, name := range names {
			ti, err := h.store.TableInfo(name)
			if err != nil {
				continue
			}
			var cols []Column
			if ti.Schema != nil {
				cols = ti.Schema.Columns
			}
			data.Tables = append(data.Tables, tableData{Name: name, Count: ti.Count, Columns: cols})
		}
		h.render(w, r, "index.html", data)
	case "xml":
		h.writeTablesXML(w, r)
	default:
		h.writeTablesJSON(w, r)
	}
}

func (h *Handler) handleListTables(w http.ResponseWriter, r *http.Request) {
	switch h.format(r) {
	case "xml":
		h.writeTablesXML(w, r)
	default:
		h.writeTablesJSON(w, r)
	}
}

func (h *Handler) writeTablesJSON(w http.ResponseWriter, r *http.Request) {
	names, err := h.store.ListTables()
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	out := jsonTables{Tables: []jsonTable{}}
	for _, name := range names {
		ti, err := h.store.TableInfo(name)
		if err != nil {
			continue
		}
		var sc *TableSchema
		if ti.Schema != nil {
			sc = schemaJSON(ti.Schema)
		}
		out.Tables = append(out.Tables, jsonTable{Name: name, Count: ti.Count, Schema: sc})
	}
	h.writeJSON(w, http.StatusOK, out)
}

func (h *Handler) writeTablesXML(w http.ResponseWriter, r *http.Request) {
	names, err := h.store.ListTables()
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	out := xmlTables{Tables: []xmlTableInfo{}}
	for _, name := range names {
		ti, err := h.store.TableInfo(name)
		if err != nil {
			continue
		}
		info := xmlTableInfo{Name: name, Count: ti.Count}
		if ti.Schema != nil {
			for _, c := range ti.Schema.Columns {
				info.Columns = append(info.Columns, xmlColumnDef{Name: c.Name, Type: c.Type, Default: c.Default})
			}
		}
		out.Tables = append(out.Tables, info)
	}
	h.writeXML(w, http.StatusOK, out)
}

func (h *Handler) handleCreateTable(w http.ResponseWriter, r *http.Request) {
	table, cols, err := h.readTableSpec(r)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if table == "" {
		h.writeError(w, r, http.StatusBadRequest, "missing table name")
		return
	}
	if err := h.store.UpdateSchema(table, cols); err != nil {
		h.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	switch h.format(r) {
	case "html":
		http.Redirect(w, r, h.url("/table/"+url.PathEscape(table)), http.StatusSeeOther)
	case "xml":
		h.writeXML(w, http.StatusOK, xmlMessage{Status: "ok", Message: "schema created"})
	default:
		h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "table": table})
	}
}

func (h *Handler) readTableSpec(r *http.Request) (string, []Column, error) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		dec := json.NewDecoder(r.Body)
		var req struct {
			Table   string `json:"table"`
			Columns []struct {
				Name    string  `json:"name"`
				Type    string  `json:"type"`
				Default *string `json:"default"`
			} `json:"columns"`
		}
		if err := dec.Decode(&req); err != nil {
			return "", nil, fmt.Errorf("invalid JSON body: %v", err)
		}
		var cols []Column
		for _, c := range req.Columns {
			typ := c.Type
			if typ == "" {
				typ = "text"
			}
			cols = append(cols, Column{Name: c.Name, Type: typ, Default: c.Default})
		}
		return req.Table, cols, nil
	}
	if err := r.ParseForm(); err != nil {
		return "", nil, err
	}
	table := r.Form.Get("_table")
	var cols []Column
	for i := 0; ; i++ {
		name := r.Form.Get(fmt.Sprintf("_cname_%d", i))
		if name == "" {
			break
		}
		typ := r.Form.Get(fmt.Sprintf("_ctype_%d", i))
		if typ == "" {
			typ = "text"
		}
		defRaw := r.Form.Get(fmt.Sprintf("_cdefault_%d", i))
		var def *string
		if defRaw != "" {
			d := defRaw
			def = &d
		}
		cols = append(cols, Column{Name: name, Type: typ, Default: def})
	}
	return table, cols, nil
}

func (h *Handler) handleSchema(w http.ResponseWriter, r *http.Request, table string) {
	if r.Method != http.MethodGet {
		h.methodNotAllowed(w, r)
		return
	}
	sc, err := h.store.EffectiveSchema(table)
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	switch h.format(r) {
	case "xml":
		if sc != nil {
			h.writeXML(w, http.StatusOK, sc)
			return
		}
		h.writeXML(w, http.StatusOK, xmlMessage{Status: "ok", Message: "no schema"})
	case "html":
		h.render(w, r, "message.html", messageData{
			Mount: h.mount, Title: "Schema", Message: "Use the JSON or XML API for schema data.",
		})
	default:
		if sc == nil {
			h.writeJSON(w, http.StatusOK, map[string]string{"table": table, "columns": "[]"})
			return
		}
		h.writeJSON(w, http.StatusOK, schemaJSON(sc))
	}
}

func (h *Handler) handleTable(w http.ResponseWriter, r *http.Request, table string) {
	switch r.Method {
	case http.MethodGet:
		h.queryTable(w, r, table)
	case http.MethodPost, http.MethodPut:
		h.submitForm(w, r, table)
	default:
		h.methodNotAllowed(w, r)
	}
}

func (h *Handler) queryTable(w http.ResponseWriter, r *http.Request, table string) {
	q := r.URL.Query()
	reserved := map[string]bool{
		"format": true, "limit": true, "offset": true,
		"orderby": true, "order": true, "dir": true, "sql": true, "q": true,
	}
	var preds []Predicate
	for k, vs := range q {
		if reserved[k] || len(vs) == 0 {
			continue
		}
		preds = append(preds, Predicate{Field: k, Op: OpEq, Value: vs[0], HasValue: true})
	}
	var cond Cond
	switch len(preds) {
	case 0:
		cond = nil
	case 1:
		cond = PredCond{preds[0]}
	default:
		cond = AndCond{L: PredCond{preds[0]}, R: AndCond{L: PredCond{preds[1]}, R: PredCond{preds[2]}}}
		for _, p := range preds[3:] {
			cond = AndCond{L: cond, R: PredCond{p}}
		}
	}

	opts := &QueryOptions{}
	if ob := firstOf(q, "orderby", "order"); ob != "" {
		opts.OrderBy = ob
		opts.Desc = strings.EqualFold(firstEmpty(q, "dir"), "desc")
	}
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			opts.Limit = n
		}
	}
	if o := q.Get("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil && n > 0 {
			opts.Offset = n
		}
	}

	recs, err := h.store.Query(table, cond, opts)
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}

	// Free-text 'q' filter across all fields.
	needle := q.Get("q")
	if needle != "" {
		lower := strings.ToLower(needle)
		filtered := recs[:0]
		for _, rec := range recs {
			for _, f := range rec.Fields {
				if strings.Contains(strings.ToLower(f.Value), lower) {
					filtered = append(filtered, rec)
					break
				}
			}
		}
		recs = filtered
	}

	switch h.format(r) {
	case "html":
		sc, err := h.store.EffectiveSchema(table)
		if err != nil {
			sc = nil
		}
		var cols []Column
		if sc != nil {
			cols = sc.Columns
		}
		filters := map[string]string{}
		for k, vs := range q {
			if reserved[k] || len(vs) == 0 {
				continue
			}
			filters[k] = vs[0]
		}
		off := opts.Offset
		lim := opts.Limit
		data := tablePageData{
			Mount:   h.mount,
			Table:   table,
			Href:    h.url("/table/" + url.PathEscape(table)),
			Schema:  cols,
			Filters: filters,
			Records: recs,
			Offset:  off,
			Limit:   lim,
			Q:       q.Get("q"),
			OrderBy: opts.OrderBy,
			Dir:     q.Get("dir"),
			Notice:  r.URL.Query().Get("notice"),
		}
		pageURL := func(nextOff int) string {
			v := url.Values{}
			for k, ks := range q {
				if k == "offset" || k == "format" || k == "notice" {
					continue
				}
				for _, x := range ks {
					v.Add(k, x)
				}
			}
			if lim > 0 {
				v.Set("limit", strconv.Itoa(lim))
			}
			v.Set("offset", strconv.Itoa(nextOff))
			return data.Href + "?" + v.Encode()
		}
		if len(recs) >= lim && lim > 0 {
			data.NextOff = off + lim
			data.NextURL = pageURL(data.NextOff)
		}
		data.PrevOff = off - lim
		if data.PrevOff < 0 {
			data.PrevOff = 0
		}
		if data.PrevOff < off {
			data.PrevURL = pageURL(data.PrevOff)
		}
		h.render(w, r, "table.html", data)
	case "xml":
		out := xmlRecords{Table: table, Count: len(recs), Records: []Record{}}
		for _, rec := range recs {
			out.Records = append(out.Records, *rec)
		}
		h.writeXML(w, http.StatusOK, out)
	default:
		out := jsonRecordList{Count: len(recs), Records: []jsonRecord{}}
		for _, rec := range recs {
			out.Records = append(out.Records, recordJSON(rec))
		}
		h.writeJSON(w, http.StatusOK, out)
	}
}

func (h *Handler) submitForm(w http.ResponseWriter, r *http.Request, table string) {
	vals, err := h.parseBody(r)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	action := vals["_action"]
	delete(vals, "_action")
	delete(vals, "format")
	id := vals["id"]
	if id == "" {
		id = vals["_id"]
	}
	delete(vals, "id")
	delete(vals, "_id")
	if action == "delete" {
		if id == "" {
			h.writeError(w, r, http.StatusBadRequest, "record id required for delete (use id= or _id=)")
			return
		}
		if err := h.store.Delete(table, id); err != nil {
			if err == ErrNotFound {
				h.writeError(w, r, http.StatusNotFound, err.Error())
				return
			}
			h.writeError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		h.respondSubmit(w, r, table, nil, false)
		return
	}
	rec, created, err := h.store.Upsert(table, id, vals)
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	h.respondSubmit(w, r, table, rec, created)
}

func (h *Handler) respondSubmit(w http.ResponseWriter, r *http.Request, table string, rec *Record, created bool) {
	switch h.format(r) {
	case "html":
		http.Redirect(w, r, h.url("/table/"+url.PathEscape(table))+"?notice="+url.QueryEscape(submitNotice(rec, created)), http.StatusSeeOther)
	case "xml":
		if rec != nil {
			h.writeXML(w, http.StatusOK, xmlUpsertResult{Status: "ok", ID: rec.ID, Created: created})
			return
		}
		h.writeXML(w, http.StatusOK, xmlMessage{Status: "ok", Message: "deleted"})
	default:
		if rec != nil {
			h.writeJSON(w, http.StatusOK, map[string]interface{}{
				"status": "ok", "id": rec.ID, "created": created, "record": recordJSON(rec),
			})
			return
		}
		h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "deleted"})
	}
}

func submitNotice(rec *Record, created bool) string {
	switch {
	case rec == nil:
		return "record deleted"
	case created:
		return "record created"
	default:
		return "record updated"
	}
}

func (h *Handler) handleRecord(w http.ResponseWriter, r *http.Request, table, id string) {
	switch r.Method {
	case http.MethodGet:
		rec, err := h.store.Get(table, id)
		if err != nil {
			if err == ErrNotFound {
				h.writeError(w, r, http.StatusNotFound, err.Error())
				return
			}
			h.writeError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		switch h.format(r) {
		case "html":
			sc, _ := h.store.EffectiveSchema(table)
			var cols []Column
			if sc != nil {
				cols = sc.Columns
			}
			data := recordPageData{
				Mount: h.mount, Table: table, ID: id,
				Href:   h.url("/record/" + url.PathEscape(table) + "/" + url.PathEscape(id)),
				Record: rec, Schema: cols,
			}
			if xmlData, err := encodeRecord(rec); err == nil {
				data.XML = string(xmlData)
			}
			h.render(w, r, "record.html", data)
		case "xml":
			h.writeXML(w, http.StatusOK, rec)
		default:
			h.writeJSON(w, http.StatusOK, recordJSON(rec))
		}
	case http.MethodPut, http.MethodPost:
		vals, err := h.parseBody(r)
		if err != nil {
			h.writeError(w, r, http.StatusBadRequest, err.Error())
			return
		}
		action := vals["_action"]
		if action == "" {
			action = "update"
		}
		delete(vals, "_action")
		delete(vals, "format")
		delete(vals, "_id")
		if action == "delete" {
			if err := h.store.Delete(table, id); err != nil {
				h.writeError(w, r, http.StatusInternalServerError, err.Error())
				return
			}
			h.respondSubmit(w, r, table, nil, false)
			return
		}
		rec, err := h.store.Update(table, id, vals)
		if err != nil {
			if err == ErrNotFound {
				h.writeError(w, r, http.StatusNotFound, err.Error())
				return
			}
			h.writeError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		h.respondSubmit(w, r, table, rec, false)
	case http.MethodDelete:
		if err := h.store.Delete(table, id); err != nil {
			if err == ErrNotFound {
				h.writeError(w, r, http.StatusNotFound, err.Error())
				return
			}
			h.writeError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		h.respondSubmit(w, r, table, nil, false)
	default:
		h.methodNotAllowed(w, r)
	}
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	tables, records := h.store.ix.Counts()
	lastSync, syncCount := h.indexer.Status()
	lastNorm, normCount := h.normalizer.Status()
	now := time.Now()
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":         "ok",
		"service":        "pod",
		"version":        Version,
		"uptime_seconds": int(now.Sub(h.started).Seconds()),
		"base":           h.store.Base(),
		"index": map[string]interface{}{
			"tables":            tables,
			"records":           records,
			"last_sync":         lastSync.UTC().Format(time.RFC3339),
			"last_sync_touched": syncCount,
		},
		"normalizer": map[string]interface{}{
			"last_run":   lastNorm.UTC().Format(time.RFC3339),
			"normalized": normCount,
		},
	})
}

func (h *Handler) handleNormalize(w http.ResponseWriter, r *http.Request) {
	count, err := h.normalizer.RunOnce()
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	switch h.format(r) {
	case "xml":
		h.writeXML(w, http.StatusOK, normalizeResult{Status: "ok", Normalized: count})
	case "html":
		h.render(w, r, "message.html", messageData{
			Mount: h.mount, Title: "Normalizer", Message: fmt.Sprintf("Upgraded %d records to the current schema.", count),
		})
	default:
		h.writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "normalized": count})
	}
}

func (h *Handler) handleReindex(w http.ResponseWriter, r *http.Request) {
	if err := h.indexer.Sync(); err != nil {
		h.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	_, touched := h.indexer.Status()
	switch h.format(r) {
	case "xml":
		h.writeXML(w, http.StatusOK, normalizeResult{Status: "ok", Normalized: touched})
	case "html":
		h.render(w, r, "message.html", messageData{
			Mount: h.mount, Title: "Indexer", Message: fmt.Sprintf("Indexed %d files.", touched),
		})
	default:
		h.writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "indexed": touched})
	}
}

type normalizeResult struct {
	XMLName    xml.Name `xml:"result"`
	Status     string   `xml:"status"`
	Normalized int      `xml:"normalized"`
}

func (h *Handler) handleSQL(w http.ResponseWriter, r *http.Request) {
	var query string
	var args []driver.Value
	if r.Method == http.MethodGet {
		query = r.URL.Query().Get("sql")
	} else {
		ct := r.Header.Get("Content-Type")
		if strings.HasPrefix(ct, "application/json") {
			dec := json.NewDecoder(r.Body)
			var req struct {
				SQL  string         `json:"sql"`
				Args []driver.Value `json:"args"`
			}
			if err := dec.Decode(&req); err != nil {
				h.writeError(w, r, http.StatusBadRequest, "invalid JSON body")
				return
			}
			query, args = req.SQL, req.Args
		} else {
			if err := r.ParseForm(); err != nil {
				h.writeError(w, r, http.StatusBadRequest, err.Error())
				return
			}
			query = r.PostFormValue("sql")
		}
	}
	if strings.TrimSpace(query) == "" {
		h.writeError(w, r, http.StatusBadRequest, "missing sql")
		return
	}
	st, err := Parse(query)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	res, rows, err := h.store.Execute(st, args)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if rows != nil {
		h.writeSQLRows(w, r, rows)
		return
	}
	var lastID, affected int64
	if res != nil {
		lastID, _ = res.LastInsertId()
		affected, _ = res.RowsAffected()
	}
	switch h.format(r) {
	case "xml":
		h.writeXML(w, http.StatusOK, xmlSQLResult{Status: "ok", LastInsert: lastID, Affected: affected})
	default:
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"status": "ok", "last_insert_id": lastID, "rows_affected": affected,
		})
	}
}

func (h *Handler) writeSQLRows(w http.ResponseWriter, r *http.Request, rows driver.Rows) {
	defer rows.Close()
	cols := rows.Columns()
	var matrix [][]driver.Value
	for {
		dest := make([]driver.Value, len(cols))
		if err := rows.Next(dest); err != nil {
			break
		}
		matrix = append(matrix, dest)
	}
	switch h.format(r) {
	case "xml":
		out := xmlSQLResult{Columns: cols}
		for _, row := range matrix {
			rw := xmlSQLRow{Values: make([]string, len(row))}
			for i, v := range row {
				if v == nil {
					rw.Values[i] = ""
				} else {
					rw.Values[i] = fmt.Sprint(v)
				}
			}
			out.Rows = append(out.Rows, rw)
		}
		h.writeXML(w, http.StatusOK, out)
	default:
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"columns": cols,
			"rows":    matrix,
		})
	}
}

type xmlSQLResult struct {
	XMLName    xml.Name    `xml:"result"`
	Status     string      `xml:"status,omitempty"`
	LastInsert int64       `xml:"last_insert_id,omitempty"`
	Affected   int64       `xml:"rows_affected,omitempty"`
	Columns    []string    `xml:"columns>column,omitempty"`
	Rows       []xmlSQLRow `xml:"rows>row,omitempty"`
}

type xmlSQLRow struct {
	Values []string `xml:"value"`
}

// --- template data ----------------------------------------------------------

type indexData struct {
	Mount  string
	Tables []tableData
}

type tableData struct {
	Name    string
	Count   int
	Columns []Column
}

type tablePageData struct {
	Mount   string
	Table   string
	Href    string // full URL to the table route (mount + path-escaped table)
	Schema  []Column
	Filters map[string]string
	Records []*Record
	Offset  int
	Limit   int
	NextOff int
	PrevOff int
	NextURL string // pagination hrefs preserving filters
	PrevURL string
	Q       string
	OrderBy string
	Dir     string
	Notice  string
}

type recordPageData struct {
	Mount  string
	Table  string
	ID     string
	Href   string // full URL to this record's route
	Record *Record
	Schema []Column
	XML    string
}

type messageData struct {
	Mount   string
	Title   string
	Message string
}

// firstOf returns the first non-empty query value among the given keys.
func firstOf(q url.Values, keys ...string) string {
	for _, k := range keys {
		if v := q.Get(k); v != "" {
			return v
		}
	}
	return ""
}

// firstEmpty returns the value of the first key present, else "".
func firstEmpty(q url.Values, keys ...string) string {
	for _, k := range keys {
		if v, ok := q[k]; ok && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}
