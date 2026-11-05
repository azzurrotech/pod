package pod

import (
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"
)

// POD-MW-001: Test handler
func TestPOD_MW_001_Handler(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("POD-MW-001: %v", err)
	}
	h := NewHandler(HandlerOptions{Store: st})
	if h == nil {
		t.Fatal("POD-MW-001: handler nil")
	}
}

// POD-FORM-001: Test form submission
func TestPOD_FORM_001_PostFormCreate(t *testing.T) {
	st, _ := Open(t.TempDir())
	h := NewHandler(HandlerOptions{Store: st})
	form := strings.NewReader("name=Alice&email=alice@example.com")
	req := httptest.NewRequest("POST", "/pod/table/contacts", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 303 && rec.Code != 200 && rec.Code != 201 {
		t.Fatalf("POD-FORM-001: unexpected status %d", rec.Code)
	}
}

// POD-GET-001: Test table query
func TestPOD_GET_001_GetTable(t *testing.T) {
	st, _ := Open(t.TempDir())
	h := NewHandler(HandlerOptions{Store: st})
	req := httptest.NewRequest("GET", "/pod/table/contacts", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("POD-GET-001: expected 200, got %d", rec.Code)
	}
}

// POD-GET-002: Test record card
func TestPOD_GET_002_GetRecordCard(t *testing.T) {
	st, _ := Open(t.TempDir())
	h := NewHandler(HandlerOptions{Store: st})
	form := strings.NewReader("name=Bob&_id=123")
	req := httptest.NewRequest("POST", "/pod/table/people", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	req2 := httptest.NewRequest("GET", "/pod/record/people/123", nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("POD-GET-002: expected 200, got %d", rec2.Code)
	}
}

// POD-SCH-001: Test auto schema
func TestPOD_SCH_001_AutoSchema(t *testing.T) {
	st, _ := Open(t.TempDir())
	h := NewHandler(HandlerOptions{Store: st})
	req := httptest.NewRequest("GET", "/pod/schema/test", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("POD-SCH-001: expected 200, got %d", rec.Code)
	}
}

// POD-SQL-001: Test SQL driver
func TestPOD_SQL_001_Driver(t *testing.T) {
	db, err := sql.Open("pod", t.TempDir())
	if err != nil {
		t.Fatalf("POD-SQL-001: %v", err)
	}
	if db != nil {
		db.Close()
	}
}
