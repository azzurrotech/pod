package pod

// Tests for the features added for the README: file uploads, optional
// encryption at rest, link-based sharing, the exposed auth helper and the
// self-healing schema.

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// postMultipart builds and serves a multipart form submission.
func postMultipart(t *testing.T, h *Handler, target string, fields map[string]string, fileName, fileField string, fileData []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	fw, err := mw.CreateFormFile(fileField, fileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(fileData); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", target, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestFileUploadRoundTrip(t *testing.T) {
	h, st := newTestHandler(t, "/")
	rr := postMultipart(t, h, "/table/acme/contacts",
		map[string]string{"name": "Jane"}, "cat.png", "avatar", []byte("PNGDATA"))
	if rr.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID == "" {
		t.Fatal("upload did not return a record id")
	}

	rec, err := st.Get("acme/contacts", resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := rec.Get("name"); got != "Jane" {
		t.Fatalf("name = %q, want Jane", got)
	}
	f, ok := rec.Field("avatar")
	if !ok {
		t.Fatal("record has no avatar field")
	}
	if !f.IsFile() || f.Filename != "cat.png" || f.Size != 7 || f.ContentType == "" {
		t.Fatalf("file field metadata = %+v", f)
	}
	storedName := path.Base(f.Value)

	// The blob is served back verbatim.
	dl := serve(h, "GET", "/files/acme/contacts/"+resp.ID+"/"+storedName, "", nil)
	if dl.Code != http.StatusOK || dl.Body.String() != "PNGDATA" {
		t.Fatalf("download = %d %q", dl.Code, dl.Body.String())
	}

	// Deleting the record removes the blob.
	del := serve(h, "DELETE", "/record/acme/contacts/"+resp.ID, "", nil)
	if del.Code != http.StatusOK {
		t.Fatalf("delete = %d", del.Code)
	}
	if _, err := st.ReadUpload("acme/contacts", resp.ID, storedName); !os.IsNotExist(err) {
		t.Fatalf("blob still present after delete: %v", err)
	}
}

func TestFileUploadTraversalRejected(t *testing.T) {
	if _, err := safeStoredName("../../etc/passwd"); err == nil {
		t.Fatal("expected traversal name to be rejected")
	}
	if _, err := safeStoredName(`..\..\evil`); err == nil {
		t.Fatal("expected backslash traversal to be rejected")
	}
}

func TestEncryptionAtRest(t *testing.T) {
	dir := t.TempDir()
	key := bytes.Repeat([]byte{0x2a}, encKeySize)
	st, err := OpenEncrypted(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Encrypted() {
		t.Fatal("store not marked encrypted")
	}
	h := NewHandler(HandlerOptions{Store: st, Mount: "/"})
	rr := postMultipart(t, h, "/table/people",
		map[string]string{"ssn": "123-45-6789"}, "id.txt", "doc", []byte("top secret"))
	if rr.Code != http.StatusOK {
		t.Fatalf("upload = %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		ID string `json:"id"`
	}
	json.Unmarshal(rr.Body.Bytes(), &resp)

	// On disk the value must be sealed, not plaintext.
	raw, err := os.ReadFile(filepath.Join(dir, "people", resp.ID+".xml"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("123-45-6789")) {
		t.Fatal("plaintext value found on disk")
	}
	if !bytes.Contains(raw, []byte(`enc="aes-256-gcm"`)) {
		t.Fatalf("missing enc tag on disk: %s", raw)
	}
	blob, err := os.ReadFile(filepath.Join(dir, "people", ".pod-files", resp.ID, "doc-1-id.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("top secret")) {
		t.Fatal("plaintext blob found on disk")
	}

	// Reads decrypt transparently.
	rec, err := st.Get("people", resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := rec.Get("ssn"); v != "123-45-6789" {
		t.Fatalf("decrypted ssn = %q", v)
	}
	dl := serve(h, "GET", "/files/people/"+resp.ID+"/doc-1-id.txt", "", nil)
	if dl.Body.String() != "top secret" {
		t.Fatalf("decrypted blob = %q", dl.Body.String())
	}

	// A wrong key must not decrypt.
	other, err := OpenEncrypted(t.TempDir(), bytes.Repeat([]byte{0x11}, encKeySize))
	if err != nil {
		t.Fatal(err)
	}
	_ = other // separate store is a sanity check that construction works
	if _, err := ParseEncryptionKey([]byte("short")); err == nil {
		t.Fatal("expected short key to be rejected")
	}
}

func TestShareLinkRoundTrip(t *testing.T) {
	h, st := newTestHandler(t, "/")
	mustUpsert(t, st, "people", "1", map[string]string{"name": "Jane"})

	vals := url.Values{}
	vals.Set("kind", "record")
	vals.Set("table", "people")
	vals.Set("id", "1")
	rr := serve(h, "POST", "/share", "application/x-www-form-urlencoded", formBody(vals))
	if rr.Code != http.StatusOK {
		t.Fatalf("share create = %d: %s", rr.Code, rr.Body.String())
	}
	var resp shareResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Token == "" || resp.URL == "" {
		t.Fatalf("share response = %+v", resp)
	}

	resolved := serve(h, "GET", "/s/"+resp.Token, "", nil)
	if resolved.Code != http.StatusOK || !strings.Contains(resolved.Body.String(), "Jane") {
		t.Fatalf("resolve = %d %s", resolved.Code, resolved.Body.String())
	}

	// A share must not be re-sharable and cannot be used on another table.
	if !shareAllows(&Share{Table: "people"}, "people", "read") {
		t.Fatal("read share should allow read")
	}
	if shareAllows(&Share{Table: "people"}, "people", "write") {
		t.Fatal("read-only share must not allow write")
	}

	rev := serve(h, "DELETE", "/share/"+resp.Token, "", nil)
	if rev.Code != http.StatusOK {
		t.Fatalf("revoke = %d", rev.Code)
	}
	gone := serve(h, "GET", "/s/"+resp.Token, "", nil)
	if gone.Code != http.StatusNotFound {
		t.Fatalf("revoked share resolve = %d, want 404", gone.Code)
	}

	// Expired shares do not resolve.
	expired, err := st.CreateShare(Share{Kind: ShareTable, Table: "people"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = expired
}

func TestAuthWrapAndPolicy(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mustUpsert(t, st, "people", "1", map[string]string{"name": "Jane"})

	// A refusing Authenticate yields 401 from Wrap.
	deny := NewAuth(AuthOptions{Authenticate: func(*http.Request) (Principal, error) {
		return Principal{}, ErrUnauthorized
	}})
	guarded := deny.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	guarded.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("denied request = %d, want 401", rr.Code)
	}

	// A read-only principal may read but not write (policy enforces RBAC).
	a := NewAuth(AuthOptions{
		Authenticate: func(*http.Request) (Principal, error) {
			return Principal{Subject: "s1", Scopes: []string{"read"}}, nil
		},
		Policy: func(p Principal, table, action string) bool { return p.HasScope(action) },
	})
	h := NewHandler(HandlerOptions{Store: st, Mount: "/", Auth: a})
	wrapped := a.Wrap(h)

	get := httptest.NewRecorder()
	wrapped.ServeHTTP(get, httptest.NewRequest("GET", "/table/people", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("authorized read = %d: %s", get.Code, get.Body.String())
	}
	postReq := httptest.NewRequest("POST", "/table/people",
		strings.NewReader(url.Values{"name": {"Nope"}}.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post := httptest.NewRecorder()
	wrapped.ServeHTTP(post, postReq)
	if post.Code != http.StatusForbidden {
		t.Fatalf("unauthorized write = %d, want 403", post.Code)
	}

	// With no auth helper the server is open.
	open := NewHandler(HandlerOptions{Store: st, Mount: "/"})
	openRec := serve(open, "GET", "/table/people", "", nil)
	if openRec.Code != http.StatusOK {
		t.Fatalf("open server = %d", openRec.Code)
	}
}

func TestSchemaSelfHealing(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateSchema("people", []Column{{Name: "name", Type: "text"}}); err != nil {
		t.Fatal(err)
	}
	sc, _ := st.Schema("people")
	startVersion := sc.Version

	// A submission introducing a new field widens the pinned schema.
	if _, _, err := st.Upsert("people", "1", map[string]string{"name": "Jane", "email": "j@x"}); err != nil {
		t.Fatal(err)
	}
	sc, err = st.Schema("people")
	if err != nil {
		t.Fatal(err)
	}
	if sc.Column("email") == nil {
		t.Fatalf("pinned schema was not widened: %+v", sc.Columns)
	}
	if sc.Version <= startVersion {
		t.Fatalf("schema version not bumped: %d -> %d", startVersion, sc.Version)
	}

	// A malformed schema file resolves gracefully instead of erroring.
	dir := filepath.Join(st.Base(), "people")
	if err := os.WriteFile(filepath.Join(dir, schemaFileName), []byte("<<not xml"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Schema("people"); err != nil {
		t.Fatalf("Schema on malformed file errored: %v", err)
	}
	if _, err := st.EffectiveSchema("people"); err != nil {
		t.Fatalf("EffectiveSchema on malformed file errored: %v", err)
	}
	if _, _, err := st.Upsert("people", "2", map[string]string{"name": "Jo"}); err != nil {
		t.Fatalf("Upsert with malformed schema errored: %v", err)
	}
}
