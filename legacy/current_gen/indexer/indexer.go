package indexer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type IndexEntry struct {
	ID        string                 `json:"id"`
	Table     string                 `json:"table"`
	Key       string                 `json:"key"`
	Value     map[string]interface{} `json:"value"`
	Timestamp time.Time              `json:"timestamp"`
	Version   int                    `json:"version"`
}

type ColumnDefinition struct {
	Name     string      `json:"name"`
	Type     string      `json:"type"`
	Nullable bool        `json:"nullable"`
	Default  interface{} `json:"default"`
}

type TableMetadata struct {
	Name      string             `json:"name"`
	Columns   []ColumnDefinition `json:"columns"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
}

type FilesystemIndexer struct {
	basePath string
	mutex    sync.RWMutex
	tables   map[string]*TableMetadata
}

type QueryResult struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
	Count   int         `json:"count,omitempty"`
}

// NewFilesystemIndexer creates a new filesystem indexer with the specified base path
func NewFilesystemIndexer(basePath string) *FilesystemIndexer {
	indexer := &FilesystemIndexer{
		basePath: basePath,
		tables:   make(map[string]*TableMetadata),
	}

	// Create directory structure
	if err := os.MkdirAll(filepath.Join(basePath, "tables"), 0755); err != nil {
		panic(fmt.Sprintf("Failed to create tables directory: %v", err))
	}
	if err := os.MkdirAll(filepath.Join(basePath, "data"), 0755); err != nil {
		panic(fmt.Sprintf("Failed to create data directory: %v", err))
	}

	return indexer
}

// CreateTable creates a new table with the specified columns
func (idx *FilesystemIndexer) CreateTable(name string, columns []ColumnDefinition) error {
	idx.mutex.Lock()
	defer idx.mutex.Unlock()

	if _, exists := idx.tables[name]; exists {
		return fmt.Errorf("table %s already exists", name)
	}

	tablePath := filepath.Join(idx.basePath, "tables", name+".meta")

	metadata := &TableMetadata{
		Name:      name,
		Columns:   columns,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := idx.saveMetadata(tablePath, metadata); err != nil {
		return err
	}

	idx.tables[name] = metadata
	return nil
}

// Insert adds a new entry to the specified table
func (idx *FilesystemIndexer) Insert(tableName, id string, values map[string]interface{}) error {
	idx.mutex.Lock()
	defer idx.mutex.Unlock()

	if _, exists := idx.tables[tableName]; !exists {
		return fmt.Errorf("table %s does not exist", tableName)
	}

	entry := &IndexEntry{
		ID:        id,
		Table:     tableName,
		Key:       idx.generateKey(values),
		Value:     values,
		Timestamp: time.Now(),
		Version:   1,
	}

	entryPath := filepath.Join(idx.basePath, "data", tableName, id+".json")
	if err := idx.saveEntry(entryPath, entry); err != nil {
		return err
	}

	return nil
}

// Select retrieves an entry from the specified table by ID
func (idx *FilesystemIndexer) Select(tableName, id string) (*IndexEntry, error) {
	idx.mutex.RLock()
	defer idx.mutex.RUnlock()

	if _, exists := idx.tables[tableName]; !exists {
		return nil, fmt.Errorf("table %s does not exist", tableName)
	}

	entryPath := filepath.Join(idx.basePath, "data", tableName, id+".json")
	entry, err := idx.loadEntry(entryPath)
	if err != nil {
		return nil, err
	}

	return entry, nil
}

// Update modifies an existing entry in the specified table
func (idx *FilesystemIndexer) Update(tableName, id string, values map[string]interface{}) error {
	idx.mutex.Lock()
	defer idx.mutex.Unlock()

	if _, exists := idx.tables[tableName]; !exists {
		return fmt.Errorf("table %s does not exist", tableName)
	}

	entryPath := filepath.Join(idx.basePath, "data", tableName, id+".json")
	entry, err := idx.loadEntry(entryPath)
	if err != nil {
		return err
	}

	for k, v := range values {
		entry.Value[k] = v
	}

	entry.Version++
	entry.Timestamp = time.Now()

	return idx.saveEntry(entryPath, entry)
}

// Delete removes an entry from the specified table
func (idx *FilesystemIndexer) Delete(tableName, id string) error {
	idx.mutex.Lock()
	defer idx.mutex.Unlock()

	if _, exists := idx.tables[tableName]; !exists {
		return fmt.Errorf("table %s does not exist", tableName)
	}

	entryPath := filepath.Join(idx.basePath, "data", tableName, id+".json")
	if err := os.Remove(entryPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

// Query executes a query against the specified table
func (idx *FilesystemIndexer) Query(tableName string, filters map[string]interface{}) (*QueryResult, error) {
	idx.mutex.RLock()
	defer idx.mutex.RUnlock()

	if _, exists := idx.tables[tableName]; !exists {
		return nil, fmt.Errorf("table %s does not exist", tableName)
	}

	dataPath := filepath.Join(idx.basePath, "data", tableName)
	files, err := os.ReadDir(dataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &QueryResult{Success: true, Data: []interface{}{}}, nil
		}
		return nil, err
	}

	var results []interface{}
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}

		entryPath := filepath.Join(dataPath, file.Name())
		entry, err := idx.loadEntry(entryPath)
		if err != nil {
			continue
		}

		if idx.matchesFilters(entry, filters) {
			results = append(results, entry.Value)
		}
	}

	return &QueryResult{Success: true, Data: results, Count: len(results)}, nil
}

// LoadMetadata retrieves metadata for a table
func (idx *FilesystemIndexer) LoadMetadata(name string) (*TableMetadata, error) {
	idx.mutex.RLock()
	defer idx.mutex.RUnlock()

	return idx.tables[name], nil
}

func (idx *FilesystemIndexer) saveMetadata(path string, metadata *TableMetadata) error {
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}

	return nil
}

// Unexported function - unused

// Unexported function - removed
// func (idx *FilesystemIndexer) loadMetadata(path string, _ interface{}) error {
//	return nil
// }

func (idx *FilesystemIndexer) saveEntry(path string, entry *IndexEntry) error {
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}

	return nil
}

func (idx *FilesystemIndexer) loadEntry(path string) (*IndexEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var entry IndexEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, err
	}

	return &entry, nil
}

func (idx *FilesystemIndexer) generateKey(values map[string]interface{}) string {
	if len(values) == 0 {
		return ""
	}

	var keys []string
	for k := range values {
		keys = append(keys, k)
	}
	return strings.Join(keys, "|")
}

func (idx *FilesystemIndexer) matchesFilters(entry *IndexEntry, filters map[string]interface{}) bool {
	for key, filterValue := range filters {
		if entryValue, exists := entry.Value[key]; !exists || entryValue != filterValue {
			return false
		}
	}
	return true
}

// Helper functions for working with form data
func getStringOrEmpty(data map[string]interface{}, key string) string {
	if value, exists := data[key]; exists {
		if str, ok := value.(string); ok {
			return str
		}
	}
	return ""
}

func getTimeOrNow(data map[string]interface{}, key string) time.Time {
	if value, exists := data[key]; exists {
		if t, ok := value.(time.Time); ok {
			return t
		}
	}
	return time.Now()
}

// FormEntry represents a form entry for the pod database
// This is used by the PodDatabaseManager interface
// Note: This struct is simpler than IndexEntry since it focuses on forms specifically
type FormEntry struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// PodDatabaseManager interface defines the contract for database operations
// This interface is implemented by FilesystemIndexer to provide the functionality
// expected by the main.go application
// The interface uses FormEntry as the data structure for forms
type PodDatabaseManager interface {
	// GetAllForms retrieves all forms from the database
	GetAllForms() ([]FormEntry, error)

	// CreateForm creates a new form with the specified ID, name, and description
	CreateForm(id, name, description string) error

	// GetForm retrieves a specific form by its ID
	GetForm(formID string) (*FormEntry, error)

	// UpdateForm updates an existing form's name and description
	UpdateForm(formID, name, description string) error

	// DeleteForm deletes a form by its ID
	DeleteForm(formID string) error
}

// Ensure FilesystemIndexer implements PodDatabaseManager
// This is done implicitly by the method signatures matching the interface
// FilesystemIndexer already has methods that match the required interface

// PodDatabaseManager implementation
// The FilesystemIndexer struct already implements the PodDatabaseManager interface
// through its methods, but we need to bridge the gap between IndexEntry and FormEntry

// GetAllForms retrieves all forms from the filesystem indexer
// This method queries all tables and converts IndexEntry objects to FormEntry
func (idx *FilesystemIndexer) GetAllForms() ([]FormEntry, error) {
	var forms []FormEntry

	// Check if forms table exists
	if _, exists := idx.tables["forms"]; !exists {
		// Create forms table if it doesn't exist
		if err := idx.CreateTable("forms", []ColumnDefinition{
			{Name: "id", Type: "string", Nullable: false},
			{Name: "name", Type: "string", Nullable: false},
			{Name: "description", Type: "string", Nullable: true},
			{Name: "created_at", Type: "time.Time", Nullable: false},
			{Name: "updated_at", Type: "time.Time", Nullable: false},
		}); err != nil {
			return nil, err
		}
	}

	// Use Query to get all form entries
	result, err := idx.Query("forms", nil)
	if err != nil {
		return nil, err
	}

	if result.Data != nil {
		for _, value := range result.Data.([]interface{}) {
			if formData, ok := value.(map[string]interface{}); ok {
				form := FormEntry{
					ID:          getStringOrEmpty(formData, "id"),
					Name:        getStringOrEmpty(formData, "name"),
					Description: getStringOrEmpty(formData, "description"),
					CreatedAt:   getTimeOrNow(formData, "created_at"),
					UpdatedAt:   getTimeOrNow(formData, "updated_at"),
				}
				forms = append(forms, form)
			}
		}
	}

	return forms, nil
}

// CreateForm creates a new form entry in the filesystem indexer
func (idx *FilesystemIndexer) CreateForm(id, name, description string) error {
	if _, exists := idx.tables["forms"]; !exists {
		// Create forms table if it doesn't exist
		if err := idx.CreateTable("forms", []ColumnDefinition{
			{Name: "id", Type: "string", Nullable: false},
			{Name: "name", Type: "string", Nullable: false},
			{Name: "description", Type: "string", Nullable: true},
			{Name: "created_at", Type: "time.Time", Nullable: false},
			{Name: "updated_at", Type: "time.Time", Nullable: false},
		}); err != nil {
			return err
		}
	}

	// Check if form already exists
	if _, err := idx.Select("forms", id); err == nil {
		return fmt.Errorf("form %s already exists", id)
	}

	// Create a form entry in the filesystem
	formData := map[string]interface{}{
		"id":          id,
		"name":        name,
		"description": description,
		"created_at":  time.Now(),
		"updated_at":  time.Now(),
	}

	return idx.Insert("forms", id, formData)
}

// GetForm retrieves a specific form by ID
func (idx *FilesystemIndexer) GetForm(formID string) (*FormEntry, error) {
	// Query the forms table using the existing Select method
	entry, err := idx.Select("forms", formID)
	if err != nil {
		return nil, fmt.Errorf("form %s not found", formID)
	}

	if entry == nil {
		return nil, fmt.Errorf("form %s not found", formID)
	}

	form := &FormEntry{
		ID:          entry.ID,
		Name:        entry.Value["name"].(string),
		Description: getStringOrEmpty(entry.Value, "description"),
		CreatedAt:   entry.Timestamp,
		UpdatedAt:   getTimeOrNow(entry.Value, "updated_at"),
	}

	return form, nil
}

// UpdateForm updates an existing form
func (idx *FilesystemIndexer) UpdateForm(formID, name, description string) error {
	// Check if form exists
	entry, err := idx.Select("forms", formID)
	if err != nil {
		return fmt.Errorf("form %s not found for update", formID)
	}

	// Update form entry
	formData := map[string]interface{}{
		"id":          formID,
		"name":        name,
		"description": description,
		"created_at":  entry.Timestamp,
		"updated_at":  time.Now(),
	}

	return idx.Update("forms", formID, formData)
}

// DeleteForm deletes a form by ID
func (idx *FilesystemIndexer) DeleteForm(formID string) error {
	// Check if form exists
	_, err := idx.Select("forms", formID)
	if err != nil {
		return fmt.Errorf("form %s not found for deletion", formID)
	}

	return idx.Delete("forms", formID)
}
