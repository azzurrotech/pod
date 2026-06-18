package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type FormSchema struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	Action    string              `json:"action"`
	Method    string              `json:"method"`
	Fields    []FormField         `json:"fields"`
	CreatedAt string              `json:"created_at"`
}

type FormField struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
}

type FormSubmission struct {
	ID        string            `json:"id"`
	FormID    string            `json:"form_id"`
	Data      map[string]string `json:"data"`
	CreatedAt string            `json:"created_at"`
}

var (
	formsDir = "forms"
	mu       sync.RWMutex
)

func main() {
	os.MkdirAll(formsDir, 0755)
	os.MkdirAll(filepath.Join(formsDir, "schemas"), 0755)
	os.MkdirAll(filepath.Join(formsDir, "submissions"), 0755)

	http.HandleFunc("/", rootHandler)
	http.HandleFunc("/form/", formHandler)
	http.HandleFunc("/submit", submitHandler)
	http.HandleFunc("/submissions/", submissionsHandler)
	http.HandleFunc("/api/forms", apiCreateForm)
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	http.ListenAndServe(":8082", nil)
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	schemas := listSchemas()
	tmpl := template.Must(template.ParseFiles("templates/index.html"))
	tmpl.Execute(w, schemas)
}

func formHandler(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/form/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	schema := loadSchema(id)
	if schema == nil {
		http.NotFound(w, r)
		return
	}
	tmpl := template.Must(template.ParseFiles("templates/form.html"))
	tmpl.Execute(w, schema)
}

func submitHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	formID := r.FormValue("_form_id")
	if formID == "" {
		http.Error(w, "missing form id", http.StatusBadRequest)
		return
	}
	schema := loadSchema(formID)
	if schema == nil {
		http.Error(w, "form not found", http.StatusNotFound)
		return
	}
	data := make(map[string]string)
	for _, f := range schema.Fields {
		data[f.Name] = r.FormValue(f.Name)
	}
	sub := FormSubmission{
		ID:        fmt.Sprintf("sub_%d", time.Now().UnixNano()),
		FormID:    formID,
		Data:      data,
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	mu.Lock()
	subDir := filepath.Join(formsDir, "submissions", formID)
	os.MkdirAll(subDir, 0755)
	subData, _ := json.Marshal(sub)
	os.WriteFile(filepath.Join(subDir, sub.ID+".json"), subData, 0644)
	mu.Unlock()
	http.Redirect(w, r, "/submissions/"+formID, http.StatusSeeOther)
}

func submissionsHandler(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/submissions/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	schema := loadSchema(id)
	if schema == nil {
		http.NotFound(w, r)
		return
	}
	mu.RLock()
	subDir := filepath.Join(formsDir, "submissions", id)
	entries, _ := os.ReadDir(subDir)
	var submissions []FormSubmission
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			data, _ := os.ReadFile(filepath.Join(subDir, e.Name()))
			var s FormSubmission
			json.Unmarshal(data, &s)
			submissions = append(submissions, s)
		}
	}
	mu.RUnlock()
	sort.Slice(submissions, func(i, j int) bool {
		return submissions[i].CreatedAt > submissions[j].CreatedAt
	})
	tmpl := template.Must(template.ParseFiles("templates/submissions.html"))
	tmpl.Execute(w, map[string]interface{}{
		"Schema":      schema,
		"Submissions": submissions,
	})
}

func listSchemas() []FormSchema {
	mu.RLock()
	defer mu.RUnlock()
	schemaDir := filepath.Join(formsDir, "schemas")
	entries, _ := os.ReadDir(schemaDir)
	var schemas []FormSchema
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			data, _ := os.ReadFile(filepath.Join(schemaDir, e.Name()))
			var s FormSchema
			json.Unmarshal(data, &s)
			schemas = append(schemas, s)
		}
	}
	return schemas
}

func loadSchema(id string) *FormSchema {
	mu.RLock()
	defer mu.RUnlock()
	data, err := os.ReadFile(filepath.Join(formsDir, "schemas", id+".json"))
	if err != nil {
		return nil
	}
	var s FormSchema
	json.Unmarshal(data, &s)
	return &s
}

func apiCreateForm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var schema FormSchema
	body, _ := io.ReadAll(r.Body)
	json.Unmarshal(body, &schema)
	if schema.Name == "" || len(schema.Fields) == 0 {
		http.Error(w, "name and fields required", http.StatusBadRequest)
		return
	}
	if schema.ID == "" {
		schema.ID = fmt.Sprintf("form_%d", time.Now().UnixNano())
	}
	schema.Action = "/submit"
	schema.Method = "POST"
	schema.CreatedAt = time.Now().Format(time.RFC3339)
	mu.Lock()
	data, _ := json.Marshal(schema)
	os.WriteFile(filepath.Join(formsDir, "schemas", schema.ID+".json"), data, 0644)
	mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(schema)
}

func registerSchemaFromForm(r *http.Request) {
	r.ParseForm()
	name := r.FormValue("_form_name")
	if name == "" {
		return
	}
	var fields []FormField
	for k := range r.Form {
		if strings.HasPrefix(k, "_") {
			continue
		}
		fieldType := r.FormValue("_type_" + k)
		if fieldType == "" {
			fieldType = "text"
		}
		fields = append(fields, FormField{
			Name:     k,
			Type:     fieldType,
			Label:    k,
			Required: true,
		})
	}
	if len(fields) == 0 {
		return
	}
	id := fmt.Sprintf("form_%d", time.Now().UnixNano())
	schema := FormSchema{
		ID:        id,
		Name:      name,
		Action:    "/submit",
		Method:    "POST",
		Fields:    fields,
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	mu.Lock()
	data, _ := json.Marshal(schema)
	os.WriteFile(filepath.Join(formsDir, "schemas", id+".json"), data, 0644)
	mu.Unlock()
}
