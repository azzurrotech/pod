// Package main implements the pod project - HTML form-based database system.
// Uses custom filesystem-based indexing system instead of SQLite.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	podindexer "azzurrotech/pod/legacy/current_gen/indexer"
)

// Server struct for the HTTP server.
type Server struct {
	port         string
	sqliteDBPath string
}

func main() {
	port := flag.String("port", "8080", "Port to listen on")
	dbPath := flag.String("db", "./data.db", "Path to database (used for filesystem base)")
	help := flag.Bool("help", false, "Show help message")
	version := flag.Bool("version", false, "Show version information")

	flag.Parse()

	if *help {
		fmt.Println("Usage: pod [options]")
		fmt.Println("  --port     Set the port to listen on (default: 8080)")
		fmt.Println("  --db       Set the path to database directory (default: ./data.db)")
		fmt.Println("  --help     Show this help message")
		fmt.Println("  --version  Show version information")
		os.Exit(0)
	}

	if *version {
		fmt.Println("POD Server v1.0.0")
		fmt.Println("Copyright 2025 Azzurro Technology Inc.")
		fmt.Println("Uses custom filesystem-based indexing system")
		os.Exit(0)
	}

	server := &Server{port: *port, sqliteDBPath: *dbPath}
	if err := server.Start(); err != nil {
		fmt.Printf("Server error: %v\n", err)
		os.Exit(1)
	}
}

// Start initializes the Pod server with filesystem database
func (s *Server) Start() error {
	fmt.Printf("Starting POD server on port %s\n", s.port)
	fmt.Println("POD - HTML Form Database Server (Filesystem-based)")

	// Initialize filesystem database manager
	dbManager := podindexer.NewFilesystemIndexer(s.sqliteDBPath)

	// Set up routes with filesystem database
	http.HandleFunc("/api/forms", s.handleForms(dbManager))
	http.HandleFunc("/api/forms/", s.handleFormWithID(dbManager))
	http.HandleFunc("/api/submit", s.handleFormSubmit)
	http.HandleFunc("/", s.handleServer)
	http.HandleFunc("/health", s.healthCheckHandler)

	return http.ListenAndServe(":"+s.port, http.DefaultServeMux)
}

// handleServer handles the root endpoint
func (s *Server) handleServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "POD - HTML Form Database Server (Filesystem-based)\n")
	fmt.Fprintf(w, "Available endpoints:\n")
	fmt.Fprintf(w, "  GET /      - Server status\n")
	fmt.Fprintf(w, "  GET /api/forms - List all forms\n")
	fmt.Fprintf(w, "  POST /api/forms - Create a new form\n")
	fmt.Fprintf(w, "  GET /api/forms/{id} - Get a specific form\n")
	fmt.Fprintf(w, "  POST /api/submit - Submit form data\n")
	fmt.Fprintf(w, "  GET /health - Health check\n")
}

// healthCheckHandler handles /health endpoint
func (s *Server) healthCheckHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "healthy",
		"timestamp": time.Now(),
		"service":   "pod",
		"version":   "1.0.0",
	})
}

// handleForms handles the /api/forms endpoint
func (s *Server) handleForms(dbManager *podindexer.FilesystemIndexer) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.Method {
		case "GET":
			s.getForms(w, r, dbManager)
		case "POST":
			s.createForm(w, r, dbManager)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// getForms handles GET /api/forms - retrieves all forms
func (s *Server) getForms(w http.ResponseWriter, r *http.Request, dbManager *podindexer.FilesystemIndexer) {
	forms, err := dbManager.GetAllForms()
	if err != nil {
		http.Error(w, "Failed to retrieve forms", http.StatusInternalServerError)
		return
	}

	if forms == nil {
		forms = []podindexer.FormEntry{}
	}

	json.NewEncoder(w).Encode(forms)
}

// createForm handles POST /api/forms - creates a new form
func (s *Server) createForm(w http.ResponseWriter, r *http.Request, dbManager *podindexer.FilesystemIndexer) {
	var form struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}

	if err := json.NewDecoder(r.Body).Decode(&form); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if form.ID == "" {
		form.ID = fmt.Sprintf("form_%d", time.Now().UnixNano())
	}

	if err := dbManager.CreateForm(form.ID, form.Name, form.Description); err != nil {
		http.Error(w, "Failed to create form: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Form created successfully",
		"form_id": form.ID,
	})
}

// handleFormWithID handles /api/forms/{id} endpoints
func (s *Server) handleFormWithID(dbManager *podindexer.FilesystemIndexer) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		formID := strings.TrimPrefix(r.URL.Path, "/api/forms/")
		if formID == "" {
			http.Error(w, "Form ID required", http.StatusBadRequest)
			return
		}

		switch r.Method {
		case "GET":
			s.getForm(w, r, dbManager, formID)
		case "PUT":
			s.updateForm(w, r, dbManager, formID)
		case "DELETE":
			s.deleteForm(w, r, dbManager, formID)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// getForm handles GET /api/forms/{id} - retrieves a specific form
func (s *Server) getForm(w http.ResponseWriter, r *http.Request, dbManager *podindexer.FilesystemIndexer, formID string) {
	form, err := dbManager.GetForm(formID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, "Form not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to retrieve form", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(form)
}

// updateForm handles PUT /api/forms/{id} - updates a form
func (s *Server) updateForm(w http.ResponseWriter, r *http.Request, dbManager *podindexer.FilesystemIndexer, formID string) {
	var form struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}

	if err := json.NewDecoder(r.Body).Decode(&form); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := dbManager.UpdateForm(formID, form.Name, form.Description); err != nil {
		http.Error(w, "Failed to update form: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Form updated successfully",
		"form_id": formID,
	})
}

// deleteForm handles DELETE /api/forms/{id} - deletes a form
func (s *Server) deleteForm(w http.ResponseWriter, r *http.Request, dbManager *podindexer.FilesystemIndexer, formID string) {
	if err := dbManager.DeleteForm(formID); err != nil {
		http.Error(w, "Failed to delete form: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Form deleted successfully",
		"form_id": formID,
	})
}

// handleFormSubmit handles POST /api/submit - processes form submissions
func (s *Server) handleFormSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var submission struct {
		FormID string            `json:"form_id"`
		Data   map[string]string `json:"data"`
	}

	if err := json.NewDecoder(r.Body).Decode(&submission); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message":       "Form submission received",
		"submission_id": fmt.Sprintf("sub_%d", time.Now().UnixNano()),
	})
}
