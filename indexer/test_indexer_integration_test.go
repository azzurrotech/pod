// Package indexer_test implements integration tests for the filesystem indexer
package indexer_test

import (
	"os"
	"testing"

	"azzurrotech/pod/indexer"
)

func TestFilesystemIndexer_CreateTable(t *testing.T) {
	basePath := "./test_fs_indexer_integration"
	indexerInstance := indexer.NewFilesystemIndexer(basePath)

	// Clean up test directory
	defer os.RemoveAll(basePath)

	// Test table creation
	if err := indexerInstance.CreateTable("test_table", []indexer.ColumnDefinition{
		{Name: "id", Type: "text", Nullable: false},
		{Name: "name", Type: "text", Nullable: false},
	}); err != nil {
		t.Errorf("Failed to create table: %v", err)
		return
	}

	// Verify table metadata
	metadata, err := indexerInstance.LoadMetadata("test_table")
	if err != nil {
		t.Errorf("Failed to load metadata: %v", err)
		return
	}

	if metadata == nil || metadata.Name != "test_table" {
		t.Error("Table metadata not correctly stored")
	}
}

func TestFilesystemIndexer_InsertAndSelect(t *testing.T) {
	basePath := "./test_fs_indexer_integration2"
	indexerInstance := indexer.NewFilesystemIndexer(basePath)

	// Clean up test directory
	defer os.RemoveAll(basePath)

	// Create test table
	if err := indexerInstance.CreateTable("users", []indexer.ColumnDefinition{
		{Name: "id", Type: "text", Nullable: false},
		{Name: "name", Type: "text", Nullable: false},
	}); err != nil {
		t.Errorf("Failed to create table: %v", err)
		return
	}

	// Test data insertion
	testData := map[string]interface{}{
		"id":   "user1",
		"name": "Test User",
	}

	if err := indexerInstance.Insert("users", "user1", testData); err != nil {
		t.Errorf("Failed to insert data: %v", err)
		return
	}

	// Test data retrieval
	entry, err := indexerInstance.Select("users", "user1")
	if err != nil {
		t.Errorf("Failed to select data: %v", err)
		return
	}

	if entry == nil || entry.ID != "user1" || entry.Value["name"] != "Test User" {
		t.Error("Data not correctly stored or retrieved")
	}

	// Test query functionality
	filters := map[string]interface{}{
		"name": "Test User",
	}

	result, err := indexerInstance.Query("users", filters)
	if err != nil {
		t.Errorf("Failed to query: %v", err)
		return
	}

	if result == nil || result.Count != 1 {
		t.Error("Query functionality not working correctly")
	}
}

func TestFilesystemIndexer_UpdateAndDelete(t *testing.T) {
	basePath := "./test_fs_indexer_integration3"
	indexerInstance := indexer.NewFilesystemIndexer(basePath)

	// Clean up test directory
	defer os.RemoveAll(basePath)

	// Create test table
	if err := indexerInstance.CreateTable("products", []indexer.ColumnDefinition{
		{Name: "id", Type: "text", Nullable: false},
		{Name: "price", Type: "float", Nullable: false},
	}); err != nil {
		t.Errorf("Failed to create table: %v", err)
		return
	}

	// Insert test data
	initialData := map[string]interface{}{
		"id":    "prod1",
		"price": 99.99,
	}

	if err := indexerInstance.Insert("products", "prod1", initialData); err != nil {
		t.Errorf("Failed to insert initial data: %v", err)
		return
	}

	// Update data
	updatedData := map[string]interface{}{
		"id":    "prod1",
		"price": 89.99,
	}

	if err := indexerInstance.Update("products", "prod1", updatedData); err != nil {
		t.Errorf("Failed to update data: %v", err)
		return
	}

	// Verify update
	entry, err := indexerInstance.Select("products", "prod1")
	if err != nil {
		t.Errorf("Failed to retrieve updated data: %v", err)
		return
	}

	if entry.Value["price"] != 89.99 {
		t.Errorf("Data not updated correctly, price: %v", entry.Value["price"])
	}

	// Delete data
	deleteErr := indexerInstance.Delete("products", "prod1")
	if deleteErr != nil {
		t.Errorf("Failed to delete data: %v", deleteErr)
		return
	}

	// Verify deletion
	_, checkErr := indexerInstance.Select("products", "prod1")
	if checkErr == nil {
		t.Error("Data should have been deleted")
	}
	_ = checkErr // Explicitly use the error variable
}

func TestPodDatabaseManagerIntegration(t *testing.T) {
	basePath := "./test_pod_manager_integration"

	// Clean up test directory
	defer os.RemoveAll(basePath)

	// Create database manager
	dbManager := indexer.NewFilesystemIndexer(basePath)

	// Test form creation
	if err := dbManager.CreateForm("form1", "Test Form", "Test Description"); err != nil {
		t.Errorf("Failed to create form: %v", err)
		return
	}

	// Test form retrieval
	form, err := dbManager.GetForm("form1")
	if err != nil {
		t.Errorf("Failed to get form: %v", err)
		return
	}

	if form == nil || form.ID != "form1" || form.Name != "Test Form" {
		t.Error("Form data not correctly stored or retrieved")
	}

	// Test get all forms
	allForms, err := dbManager.GetAllForms()
	if err != nil {
		t.Errorf("Failed to get all forms: %v", err)
		return
	}

	if len(allForms) != 1 {
		t.Errorf("Expected 1 form, got %d", len(allForms))
	}

	// Test form update
	if err := dbManager.UpdateForm("form1", "Updated Form", "Updated Description"); err != nil {
		t.Errorf("Failed to update form: %v", err)
		return
	}

	updatedForm, err := dbManager.GetForm("form1")
	if err != nil {
		t.Errorf("Failed to get updated form: %v", err)
		return
	}

	if updatedForm.Name != "Updated Form" {
		t.Errorf("Form not updated correctly, name: %s", updatedForm.Name)
	}

	// Test form deletion
	if err := dbManager.DeleteForm("form1"); err != nil {
		t.Errorf("Failed to delete form: %v", err)
		return
	}

	// Verify deletion
	_, err = dbManager.GetForm("form1")
	if err == nil {
		t.Error("Form should have been deleted")
	}
	_ = err // Explicitly use the error variable to avoid compilation warning
}
