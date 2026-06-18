package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"

	"pod"
)

func main() {
	// Register the custom driver with database/sql
	sql.Register("pod", &pod.PODDriver{})

	// Initialize the indexed store (auto-loads data and builds index)
	store := pod.NewStoreWithIndex("pod_data.xml")

	// Create HTTP handler
	handler := pod.NewHTTPHandler(store)

	// Setup route
	http.HandleFunc("/api", handler.ServeHTTP)

	fmt.Println("POD Server running on http://localhost:8080")
	fmt.Println("  POST /api          - Submit form data or SQL (via 'sql' param)")
	fmt.Println("  GET  /api          - Get all records")
	fmt.Println("  GET  /api?field=val - Filter by field")
	fmt.Println("  GET  /api?sql=...  - Execute raw SQL")

	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}
