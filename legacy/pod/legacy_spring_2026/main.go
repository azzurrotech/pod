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

// Constants
const (
	DataDir      = "storage/data"
	IndexFile    = "storage/index.json"
	StaticDir    = "static"
	TemplatesDir = "templates"
	Author       = "Azzurro Technology Inc"
	Website      = "https://azzurro.tech"
	Email        = "info@azzurro.tech"
)

// Record represents a single database entry
type Record struct {
	ID        string            `json:"id"`
	CreatedAt int64             `json:"created_at"`
	UpdatedAt int64             `json:"updated_at"`
	Data      map[string]string `json:"data"`
}

// Index represents the in-memory lookup table
type Index struct {
	Records map[string]RecordMeta `json:"records"`
	Mu      sync.RWMutex          `json:"-"`
}

// RecordMeta stores metadata for quick lookup without loading full data
type RecordMeta struct {
	ID        string   `json:"id"`
	CreatedAt int64    `json:"created_at"`
	UpdatedAt int64    `json:"updated_at"`
	Schema    []string `json:"schema"` // Keys of the data map
}

var (
	indexStore *Index
	mu         sync.Mutex
)

func init() {
	indexStore = &Index{
		Records: make(map[string]RecordMeta),
	}
	loadIndex()
}

func loadIndex() {
	data, err := os.ReadFile(IndexFile)
	if err != nil {
		if os.IsNotExist(err) {
			saveIndex()
			return
		}
		panic(fmt.Sprintf("Failed to load index: %v", err))
	}
	if err := json.Unmarshal(data, indexStore); err != nil {
		panic(fmt.Sprintf("Failed to parse index: %v", err))
	}
}

func saveIndex() {
	data, err := json.MarshalIndent(indexStore, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("Failed to marshal index: %v", err))
	}
	if err := os.WriteFile(IndexFile, data, 0644); err != nil {
		panic(fmt.Sprintf("Failed to save index: %v", err))
	}
}

func ensureDirs() {
	if err := os.MkdirAll(DataDir, 0755); err != nil {
		panic(fmt.Sprintf("Failed to create data directory: %v", err))
	}
}

// --- Handlers ---

func homeHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	tmpl := template.Must(template.ParseFiles(filepath.Join(TemplatesDir, "index.html")))
	tmpl.Execute(w, map[string]interface{}{
		"Author":  Author,
		"Website": Website,
		"Email":   Email,
	})
}

// API: Get Schema (Form Definition)
func getSchemaHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// For simplicity, we assume a default schema or fetch from the first record if exists
	// In a real app, schemas might be versioned. Here we infer from existing records.
	schema := []string{}
	for _, meta := range indexStore.Records {
		if len(meta.Schema) > len(schema) {
			schema = meta.Schema
		}
	}
	if len(schema) == 0 {
		schema = []string{"name", "email", "status"} // Default
	}
	json.NewEncoder(w).Encode(schema)
}

// API: Create/Update Record
func recordHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Error reading body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var reqData map[string]string
	if err := json.Unmarshal(body, &reqData); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	id := r.URL.Query().Get("id")
	now := time.Now().Unix()

	mu.Lock()
	defer mu.Unlock()

	var rec Record
	if id != "" {
		// Update
		rec = indexStore.Records[id].ToRecord() // Helper to reconstruct
		rec.Data = reqData
		rec.UpdatedAt = now
	} else {
		// Create
		rec = Record{
			ID:        fmt.Sprintf("%d", now),
			CreatedAt: now,
			UpdatedAt: now,
			Data:      reqData,
		}
	}

	// Save to file system
	filePath := filepath.Join(DataDir, rec.ID+".json")
	recJSON, _ := json.MarshalIndent(rec, "", "  ")
	if err := os.WriteFile(filePath, recJSON, 0644); err != nil {
		http.Error(w, "Failed to write record", http.StatusInternalServerError)
		return
	}

	// Update Index
	meta := RecordMeta{
		ID:        rec.ID,
		CreatedAt: rec.CreatedAt,
		UpdatedAt: rec.UpdatedAt,
		Schema:    getKeys(rec.Data),
	}
	indexStore.Records[rec.ID] = meta
	saveIndex()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rec)
}

// API: Search Records
func searchHandler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	results := []Record{}

	//mu.RLock()
	//defer mu.RUnlock()

	for _, meta := range indexStore.Records {
		rec := meta.ToRecord()
		match := false
		for _, v := range rec.Data {
			if strings.Contains(strings.ToLower(v), strings.ToLower(query)) {
				match = true
				break
			}
		}
		if match {
			results = append(results, rec)
		}
	}

	// Sort by updated desc
	sort.Slice(results, func(i, j int) bool {
		return results[i].UpdatedAt > results[j].UpdatedAt
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

// Helper to convert meta back to record (reads file)
func (m RecordMeta) ToRecord() Record {
	filePath := filepath.Join(DataDir, m.ID+".json")
	data, err := os.ReadFile(filePath)
	if err != nil {
		return Record{ID: m.ID}
	}
	var rec Record
	json.Unmarshal(data, &rec)
	return rec
}

func getKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func main() {
	ensureDirs()

	http.HandleFunc("/", homeHandler)
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(StaticDir))))

	// API Routes
	http.HandleFunc("/api/schema", getSchemaHandler)
	http.HandleFunc("/api/record", recordHandler)
	http.HandleFunc("/api/search", searchHandler)

	fmt.Printf("POD Server running on http://localhost:8080\n")
	fmt.Printf("Author: %s (%s)\n", Author, Website)
	fmt.Printf("Contact: %s\n", Email)
	http.ListenAndServe(":8080", nil)
}
