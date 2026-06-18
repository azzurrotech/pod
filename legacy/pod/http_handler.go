package pod

import (
	"database/sql/driver"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// HTTPHandler wraps the PODStoreWithIndex and provides HTTP endpoints
type HTTPHandler struct {
	store *PODStoreWithIndex
}

// NewHTTPHandler creates a new HTTP handler with the given store
func NewHTTPHandler(store *PODStoreWithIndex) *HTTPHandler {
	return &HTTPHandler{
		store: store,
	}
}

// newConn creates a PODConn directly from the store (same package access)
func (h *HTTPHandler) newConn() *PODConn {
	return &PODConn{store: h.store}
}

// ServeHTTP implements the http.Handler interface
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/xml")

	switch r.Method {
	case http.MethodPost:
		h.handlePost(w, r)
	case http.MethodGet:
		h.handleGet(w, r)
	default:
		h.sendError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePost processes POST requests (form data or SQL commands)
func (h *HTTPHandler) handlePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.sendError(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	// Check for SQL command in form
	if sql := r.FormValue("sql"); sql != "" {
		h.executeSQLCommand(w, sql, nil)
		return
	}

	if len(r.Form) == 0 {
		h.sendError(w, "No form data provided", http.StatusBadRequest)
		return
	}

	// Build INSERT statement from form data
	columns := make([]string, 0, len(r.Form))
	values := make([]string, 0, len(r.Form))

	for key := range r.Form {
		if key == "sql" {
			continue
		}
		columns = append(columns, key)
		values = append(values, r.FormValue(key))
	}

	var insertSQL strings.Builder
	insertSQL.WriteString(fmt.Sprintf("INSERT INTO data (%s) VALUES (", strings.Join(columns, ", ")))
	for i := range columns {
		if i > 0 {
			insertSQL.WriteString(", ")
		}
		insertSQL.WriteString("?")
	}
	insertSQL.WriteString(")")

	conn := h.newConn()

	stmt, err := conn.Prepare(insertSQL.String())
	if err != nil {
		h.sendError(w, fmt.Sprintf("SQL prepare error: %v", err), http.StatusBadRequest)
		return
	}
	defer stmt.Close()

	// Convert values to driver.Value
	driverValues := make([]driver.Value, len(values))
	for i, v := range values {
		driverValues[i] = v
	}

	result, err := stmt.Exec(driverValues)
	if err != nil {
		h.sendError(w, fmt.Sprintf("SQL execution error: %v", err), http.StatusBadRequest)
		return
	}

	id, _ := result.LastInsertId()
	response := fmt.Sprintf(`<result><status>success</status><id>%d</id></result>`, id)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(response))
}

// handleGet processes GET requests (URL params or SQL commands)
func (h *HTTPHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	// Check for SQL command in query
	if sql := query.Get("sql"); sql != "" {
		h.executeSQLCommand(w, sql, nil)
		return
	}

	if len(query) == 0 {
		h.returnAllRecords(w)
		return
	}

	// Build SELECT statement from query params
	var selectSQL strings.Builder
	selectSQL.WriteString("SELECT * FROM data WHERE ")

	conditions := make([]string, 0)
	paramValues := make([]string, 0)

	for key, vals := range query {
		if key == "sql" {
			continue
		}
		for _, v := range vals {
			conditions = append(conditions, fmt.Sprintf("%s = ?", key))
			paramValues = append(paramValues, v)
		}
	}

	selectSQL.WriteString(strings.Join(conditions, " AND "))

	conn := h.newConn()

	stmt, err := conn.Prepare(selectSQL.String())
	if err != nil {
		h.sendError(w, fmt.Sprintf("SQL prepare error: %v", err), http.StatusBadRequest)
		return
	}
	defer stmt.Close()

	driverValues := make([]driver.Value, len(paramValues))
	for i, v := range paramValues {
		driverValues[i] = v
	}

	rows, err := stmt.Query(driverValues)
	if err != nil {
		h.sendError(w, fmt.Sprintf("SQL query error: %v", err), http.StatusBadRequest)
		return
	}
	defer rows.Close()

	h.rowsToXML(w, rows)
}

// executeSQLCommand handles raw SQL commands passed via form or query
func (h *HTTPHandler) executeSQLCommand(w http.ResponseWriter, sqlCmd string, values []driver.Value) {
	conn := h.newConn()

	stmt, err := conn.Prepare(sqlCmd)
	if err != nil {
		h.sendError(w, fmt.Sprintf("SQL prepare error: %v", err), http.StatusBadRequest)
		return
	}
	defer stmt.Close()

	isSelect := strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sqlCmd)), "SELECT")

	if isSelect {
		rows, err := stmt.Query(values)
		if err != nil {
			h.sendError(w, fmt.Sprintf("SQL query error: %v", err), http.StatusBadRequest)
			return
		}
		defer rows.Close()
		h.rowsToXML(w, rows)
	} else {
		result, err := stmt.Exec(values)
		if err != nil {
			h.sendError(w, fmt.Sprintf("SQL execution error: %v", err), http.StatusBadRequest)
			return
		}

		var response string
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sqlCmd)), "INSERT") {
			id, _ := result.LastInsertId()
			response = fmt.Sprintf(`<result><status>success</status><id>%d</id></result>`, id)
		} else {
			affected, _ := result.RowsAffected()
			response = fmt.Sprintf(`<result><status>success</status><affected>%d</affected></result>`, affected)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(response))
	}
}

// returnAllRecords returns all records in the database
func (h *HTTPHandler) returnAllRecords(w http.ResponseWriter) {
	conn := h.newConn()

	stmt, err := conn.Prepare("SELECT * FROM data")
	if err != nil {
		h.sendError(w, fmt.Sprintf("SQL prepare error: %v", err), http.StatusInternalServerError)
		return
	}
	defer stmt.Close()

	rows, err := stmt.Query(nil)
	if err != nil {
		h.sendError(w, fmt.Sprintf("SQL query error: %v", err), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	h.rowsToXML(w, rows)
}

// rowsToXML converts driver.Rows to XML format
func (h *HTTPHandler) rowsToXML(w http.ResponseWriter, rows driver.Rows) {
	columns := rows.Columns()

	var xmlBuilder strings.Builder
	xmlBuilder.WriteString(xml.Header)
	xmlBuilder.WriteString("<records>\n")

	for {
		dest := make([]driver.Value, len(columns))
		for i := range dest {
			dest[i] = new(string)
		}

		err := rows.Next(dest)
		if err != nil {
			if err == io.EOF {
				break
			}
			break
		}

		xmlBuilder.WriteString("  <record>\n")
		for i, col := range columns {
			var val string
			switch v := dest[i].(type) {
			case *string:
				if v != nil {
					val = *v
				}
			case string:
				val = v
			case nil:
				val = "NULL"
			default:
				val = fmt.Sprintf("%v", v)
			}

			if val == "" {
				val = "NULL"
			}
			val = escapeXML(val)
			xmlBuilder.WriteString(fmt.Sprintf("    <field name=\"%s\">%s</field>\n", escapeXML(col), val))
		}
		xmlBuilder.WriteString("  </record>\n")
	}

	xmlBuilder.WriteString("</records>")

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(xmlBuilder.String()))
}

// sendError sends an XML error response
func (h *HTTPHandler) sendError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(code)
	errorXML := fmt.Sprintf(`<error><message>%s</message></error>`, escapeXML(message))
	w.Write([]byte(errorXML))
}

// escapeXML escapes special XML characters
func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}
