// internal/api/handler.go
package api

/*
package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"pod/internal/form"
	"pod/internal/storage"
)

// Server holds all dependencies for the HTTP handlers
type Server struct {
	db        *sql.DB
	storage   *storage.FileSystem
	mux       *http.ServeMux
	templates *template.Template
}

// PageData is the generic template execution context
type PageData struct {
	Title   string
	Content interface{}
}

// SearchResultItem holds one rendered editable form from a search
type SearchResultItem struct {
	SchemaID string
	RecordID string
	FormHTML template.HTML
}

// NewServer creates the server, parses templates, and registers all routes.
// Requires Go 1.22+ for enhanced ServeMux routing patterns.
func NewServer(db *sql.DB, store *storage.FileSystem, templateDir string) (*Server, error) {
	tmpl, err := template.ParseGlob(filepath.Join(templateDir, "*.html"))
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	s := &Server{
		db:        db,
		storage:   store,
		mux:       http.NewServeMux(),
		templates: tmpl,
	}
	s.registerRoutes()
	return s, nil
}

func (s *Server) registerRoutes() {
	// Pages
	s.mux.HandleFunc("GET /", s.handleIndex)
	s.mux.HandleFunc("GET /forms", s.handleFormList)
	s.mux.HandleFunc("GET /forms/new", s.handleFormNew)
	s.mux.HandleFunc("POST /forms", s.handleFormCreate)
	s.mux.HandleFunc("GET /forms/{id}", s.handleFormView)
	// Records
	s.mux.HandleFunc("POST /records/{schema}", s.handleRecordCreate)
	s.mux.HandleFunc("GET /records/{schema}/{id}", s.handleRecordView)
	s.mux.HandleFunc("POST /records/{schema}/{id}", s.handleRecordUpdate)
	// Search
	s.mux.HandleFunc("GET /search", s.handleSearch)
	// API
	s.mux.HandleFunc("POST /api/v1/query", s.handleApiQuery)
	s.mux.HandleFunc("GET /api/v1/schemas", s.handleApiSchemas)
	s.mux.HandleFunc("GET /api/v1/records/{schema}", s.handleApiRecords)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// --- Helpers ---

func (s *Server) render(w http.ResponseWriter, name string, data PageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("template error %s: %v", name, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func isJSONRequest(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Content-Type"), "application/json")
}

// --- Page Handlers ---

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	schemas, _ := s.storage.ListSchemas()
	s.render(w, "base.html", PageData{Title: "POD Dashboard", Content: schemas})
}

func (s *Server) handleFormList(w http.ResponseWriter, r *http.Request) {
	schemas, err := s.storage.ListSchemas()
	if err != nil {
		http.Error(w, "Failed to load schemas", http.StatusInternalServerError)
		return
	}
	s.render(w, "base.html", PageData{Title: "All Forms", Content: schemas})
}

func (s *Server) handleFormNew(w http.ResponseWriter, r *http.Request) {
	s.render(w, "form_builder.html", PageData{Title: "Create Form"})
}

func (s *Server) handleFormCreate(w http.ResponseWriter, r *http.Request) {
	var schema form.Schema

	if isJSONRequest(r) {
		if err := json.NewDecoder(r.Body).Decode(&schema); err != nil {
			writeErr(w, http.StatusBadRequest, "Invalid JSON schema")
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		schema = form.Schema{
			ID:    r.FormValue("schema_id"),
			Title: r.FormValue("schema_title"),
		}
		names := r.Form["field_name"]
		types := r.Form["field_type"]
		reqs := r.Form["field_required"]
		opts := r.Form["field_options"]
		for i, name := range names {
			if name == "" {
				continue
			}
			f := form.FieldDefinition{
				Name:     name,
				Type:     safeSlice(types, i),
				Required: i < len(reqs) && reqs[i] == "on",
			}
			if f.Type == "select" && i < len(opts) && opts[i] != "" {
				f.Options = strings.Split(opts[i], ",")
			}
			schema.Fields = append(schema.Fields, f)
		}
	}

	if err := form.ValidateSchema(&schema); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.storage.SaveSchema(&schema); err != nil {
		writeErr(w, http.StatusInternalServerError, "Failed to save schema")
		return
	}

	if isJSONRequest(r) {
		writeJSON(w, http.StatusCreated, schema)
	} else {
		http.Redirect(w, r, "/forms/"+schema.ID, http.StatusSeeOther)
	}
}

func (s *Server) handleFormView(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	schema, err := s.storage.LoadSchema(id)
	if err != nil {
		http.Error(w, "Schema not found", http.StatusNotFound)
		return
	}
	formHTML, err := form.RenderFormHTML(schema, "/records/"+id, "POST")
	if err != nil {
		http.Error(w, "Render failed", http.StatusInternalServerError)
		return
	}
	s.render(w, "record_view.html", PageData{Title: schema.Title, Content: formHTML})
}

// --- Record Handlers ---

func (s *Server) handleRecordCreate(w http.ResponseWriter, r *http.Request) {
	schemaID := r.PathValue("schema")
	schema, err := s.storage.LoadSchema(schemaID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "Schema not found")
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	formData := make(map[string]string)
	for key, vals := range r.Form {
		if key == "_schema_id" || len(vals) == 0 {
			continue
		}
		formData[key] = vals[0]
	}
	record, err := form.ExtractFormData(schema, formData)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	cols, placeholders, args := buildInsertParts(schema, record)
	q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", schemaID, cols, placeholders)
	if _, err := s.db.Exec(q, args...); err != nil {
		log.Printf("insert error: %v", err)
		writeErr(w, http.StatusInternalServerError, "Failed to create record")
		return
	}

	if isJSONRequest(r) {
		writeJSON(w, http.StatusCreated, record)
	} else {
		http.Redirect(w, r, "/forms/"+schemaID, http.StatusSeeOther)
	}
}

/*
func (s *Server) handleRecordView(w http.ResponseWriter, r *http.Request) {
	schemaID := r.PathValue("schema")
	recordID := r.PathValue("id")

	sche

*/
