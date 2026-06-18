package pod

import (
	"encoding/xml"
	"fmt"
	"os"
	"sync"
)

// Field represents a single key-value pair within a record
type Field struct {
	Name  string `xml:"name,attr"`
	Value string `xml:",chardata"`
}

// Record represents a single row in our database
type Record struct {
	ID     int     `xml:"id"`
	Fields []Field `xml:"field"`
}

// Database represents the root XML structure holding all records
type Database struct {
	XMLName xml.Name `xml:"database"`
	Records []Record `xml:"record"`
}

// PODStore manages the database file and concurrency
type PODStore struct {
	dataFile string
	mu       sync.RWMutex
}

// NewStore creates a new PODStore instance
func NewStore(dataFile string) *PODStore {
	return &PODStore{
		dataFile: dataFile,
	}
}

// loadDB reads the XML file into a Database struct
func (s *PODStore) loadDB() (*Database, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var db Database

	// Check if file exists
	if _, err := os.Stat(s.dataFile); os.IsNotExist(err) {
		return &db, nil
	}

	data, err := os.ReadFile(s.dataFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read database file: %w", err)
	}

	if len(data) == 0 {
		return &db, nil
	}

	if err := xml.Unmarshal(data, &db); err != nil {
		return nil, fmt.Errorf("failed to parse XML: %w", err)
	}

	return &db, nil
}

// saveDB writes the Database struct back to the XML file
func (s *PODStore) saveDB(db *Database) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := xml.MarshalIndent(db, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal XML: %w", err)
	}

	// Prepend XML header
	header := []byte(xml.Header)
	fullData := append(header, data...)

	if err := os.WriteFile(s.dataFile, fullData, 0644); err != nil {
		return fmt.Errorf("failed to write database file: %w", err)
	}

	return nil
}

// getNextID finds the maximum existing ID and returns the next one
func (s *PODStore) getNextID(db *Database) int {
	maxID := 0
	for _, record := range db.Records {
		if record.ID > maxID {
			maxID = record.ID
		}
	}
	return maxID + 1
}

// IndexEntry represents a mapping from a field value to record IDs
type IndexEntry struct {
	Field     string
	Value     string
	RecordIDs []int
}

// IndexMap is the in-memory index structure
// Key: "FieldName:FieldValue" -> Value: []IndexEntry
type IndexMap map[string][]IndexEntry

// PODStoreWithIndex extends PODStore with indexing capabilities
type PODStoreWithIndex struct {
	*PODStore
	index   IndexMap
	indexMu sync.RWMutex
}

// NewStoreWithIndex creates a store with indexing enabled and initializes the index
func NewStoreWithIndex(dataFile string) *PODStoreWithIndex {
	baseStore := NewStore(dataFile)
	s := &PODStoreWithIndex{
		PODStore: baseStore,
		index:    make(IndexMap),
	}

	// Auto-load and rebuild index on creation
	if db, err := s.loadDB(); err == nil {
		if db != nil {
			s.rebuildIndex(db)
		}
	}
	return s
}

// rebuildIndex scans all records and reconstructs the index
func (s *PODStoreWithIndex) rebuildIndex(db *Database) {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()

	// Clear existing index
	s.index = make(IndexMap)

	for _, record := range db.Records {
		for _, field := range record.Fields {
			key := fmt.Sprintf("%s:%s", field.Name, field.Value)

			// Check if entry exists
			existing, found := s.index[key]
			if !found {
				s.index[key] = []IndexEntry{{
					Field:     field.Name,
					Value:     field.Value,
					RecordIDs: []int{record.ID},
				}}
			} else {
				// Append ID if not already present
				foundID := false
				for i := range existing {
					if existing[i].Field == field.Name && existing[i].Value == field.Value {
						for _, id := range existing[i].RecordIDs {
							if id == record.ID {
								foundID = true
								break
							}
						}
						if !foundID {
							existing[i].RecordIDs = append(existing[i].RecordIDs, record.ID)
						}
						break
					}
				}
				if !foundID {
					s.index[key] = append(s.index[key], IndexEntry{
						Field:     field.Name,
						Value:     field.Value,
						RecordIDs: []int{record.ID},
					})
				}
			}
		}
	}
}

// getIndex retrieves record IDs for a specific field value
func (s *PODStoreWithIndex) getIndex(fieldName, fieldValue string) []int {
	s.indexMu.RLock()
	defer s.indexMu.RUnlock()

	key := fmt.Sprintf("%s:%s", fieldName, fieldValue)
	entries := s.index[key]

	if len(entries) == 0 {
		return nil
	}

	return entries[0].RecordIDs
}

// fastLookup uses the index to find records matching a specific field=value
func (s *PODStoreWithIndex) fastLookup(fieldName, fieldValue string) []int {
	return s.getIndex(fieldName, fieldValue)
}

// updateIndexForInsert adds a record to the index
func (s *PODStoreWithIndex) updateIndexForInsert(record Record) {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()

	for _, field := range record.Fields {
		key := fmt.Sprintf("%s:%s", field.Name, field.Value)

		if _, exists := s.index[key]; !exists {
			s.index[key] = []IndexEntry{{
				Field:     field.Name,
				Value:     field.Value,
				RecordIDs: []int{record.ID},
			}}
		} else {
			for i := range s.index[key] {
				if s.index[key][i].Field == field.Name && s.index[key][i].Value == field.Value {
					// Check if ID already exists
					found := false
					for _, id := range s.index[key][i].RecordIDs {
						if id == record.ID {
							found = true
							break
						}
					}
					if !found {
						s.index[key][i].RecordIDs = append(s.index[key][i].RecordIDs, record.ID)
					}
					break
				}
			}
		}
	}
}

// updateIndexForDelete removes a record from the index
func (s *PODStoreWithIndex) updateIndexForDelete(recordID int) {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()

	for key := range s.index {
		for i := range s.index[key] {
			newIDs := make([]int, 0)
			for _, id := range s.index[key][i].RecordIDs {
				if id != recordID {
					newIDs = append(newIDs, id)
				}
			}
			s.index[key][i].RecordIDs = newIDs

			if len(s.index[key][i].RecordIDs) == 0 {
				s.index[key] = append(s.index[key][:i], s.index[key][i+1:]...)
				i--
			}
		}
	}
}

// updateIndexForUpdate handles field changes (remove old, add new)
func (s *PODStoreWithIndex) updateIndexForUpdate(record Record, oldFields map[string]string) {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()

	// Remove old values
	for oldName, oldValue := range oldFields {
		key := fmt.Sprintf("%s:%s", oldName, oldValue)
		if entries, exists := s.index[key]; exists {
			for i := range entries {
				if entries[i].Field == oldName && entries[i].Value == oldValue {
					newIDs := make([]int, 0)
					for _, id := range entries[i].RecordIDs {
						if id != record.ID {
							newIDs = append(newIDs, id)
						}
					}
					entries[i].RecordIDs = newIDs
					if len(newIDs) == 0 {
						s.index[key] = append(s.index[key][:i], s.index[key][i+1:]...)
					}
					break
				}
			}
		}
	}

	// Add new values
	for _, field := range record.Fields {
		key := fmt.Sprintf("%s:%s", field.Name, field.Value)
		if _, exists := s.index[key]; !exists {
			s.index[key] = []IndexEntry{{
				Field:     field.Name,
				Value:     field.Value,
				RecordIDs: []int{record.ID},
			}}
		} else {
			for i := range s.index[key] {
				if s.index[key][i].Field == field.Name && s.index[key][i].Value == field.Value {
					found := false
					for _, id := range s.index[key][i].RecordIDs {
						if id == record.ID {
							found = true
							break
						}
					}
					if !found {
						s.index[key][i].RecordIDs = append(s.index[key][i].RecordIDs, record.ID)
					}
					break
				}
			}
		}
	}
}
