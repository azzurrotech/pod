package pod

// File uploads.
//
// <input type="file"> works out of the box. A multipart POST stores each chosen
// file next to its record under
//
//	<base>/<table>/.pod-files/<id>/<stored-name>
//
// and writes a *reference* into the record field. The field keeps the original
// file name, content type, byte size and a SHA-256 digest as XML attributes, so
// the record travels with its metadata and the blob can be served back over
// HTTP:
//
//	<field name="avatar" kind="file" filename="cat.png"
//	       content_type="image/png" size="10240" sha256="…">
//	  .pod-files/1001/avatar-1-cat.png
//	</field>
//
// Stored names are never trusted verbatim: the client name is reduced to its
// base and rejected if it still contains traversal characters, and the stored
// name is prefixed by the (sanitised) field name and a counter so re-uploads
// cannot clobber each other. All IO is atomic and, when encryption at rest is
// enabled, blobs are sealed on the way to disk and opened on the way back.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// filesDirName is the per-table directory that holds uploaded blobs. The
// leading dot keeps it out of table listings and record scans.
const filesDirName = ".pod-files"

// maxUploadSize caps a single uploaded blob (64 MiB), matching the request
// body limit.
const maxUploadSize = 64 << 20

// fileUpload is one multipart file part resolved from a request body.
type fileUpload struct {
	Field       string // form control name
	Filename    string // client-supplied name (display only)
	ContentType string // client-supplied MIME type
	Data        []byte // file bytes (plaintext)
}

// filesDir returns the blob directory for a record, creating nothing.
func (s *Store) filesDir(table, id string) (string, error) {
	dir, err := s.TablePath(table)
	if err != nil {
		return "", err
	}
	id, err = safeID(id)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("pod: empty record id")
	}
	return filepath.Join(dir, filesDirName, id), nil
}

// safeStoredName reduces a client-supplied file name to a single safe path
// segment. Traversal (a ".." segment), absolute paths, backslashes and control
// characters are rejected, exactly as the README promises. A browser's
// "C:\fakepath\cat.png" is accepted because it has no traversal and its final
// segment is the real name.
func safeStoredName(name string) (string, error) {
	raw := strings.ReplaceAll(strings.TrimSpace(name), `\`, `/`)
	if raw == "" || strings.ContainsRune(raw, 0) {
		return "", fmt.Errorf("pod: invalid file name %q", name)
	}
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("pod: absolute file name %q", name)
	}
	segs := strings.Split(raw, "/")
	for _, seg := range segs {
		if seg == ".." {
			return "", fmt.Errorf("pod: path traversal in file name %q", name)
		}
	}
	base := segs[len(segs)-1]
	if base == "" || base == "." {
		return "", fmt.Errorf("pod: invalid file name %q", name)
	}
	if strings.IndexFunc(base, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", fmt.Errorf("pod: invalid file name %q", name)
	}
	if len(base) > 200 {
		return "", fmt.Errorf("pod: file name too long")
	}
	return base, nil
}

// safeFieldName reduces a form control name to something usable as a file-name
// prefix.
func safeFieldName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "file"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		return "file"
	}
	return out
}

// StoreUpload writes one uploaded blob and returns the field that references
// it. storedName is made unique within the record's blob directory and the
// returned field value is the slash-separated path relative to the table
// directory (e.g. ".pod-files/1001/avatar-1-cat.png").
func (s *Store) StoreUpload(table, id, field, filename, contentType string, data []byte) (Field, error) {
	if len(data) > maxUploadSize {
		return Field{}, fmt.Errorf("pod: upload %q exceeds %d bytes", filename, maxUploadSize)
	}
	dir, err := s.filesDir(table, id)
	if err != nil {
		return Field{}, err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return Field{}, err
	}
	base, err := safeStoredName(filename)
	if err != nil {
		base = "file"
	}
	prefix := safeFieldName(field)
	storedName := ""
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s-%d-%s", prefix, i, base)
		if _, statErr := os.Stat(filepath.Join(dir, candidate)); os.IsNotExist(statErr) {
			storedName = candidate
			break
		}
		if i > 10000 {
			return Field{}, fmt.Errorf("pod: could not allocate a stored name for %q", filename)
		}
	}
	if err := s.writeBlob(filepath.Join(dir, storedName), data); err != nil {
		return Field{}, err
	}
	sum := sha256.Sum256(data)
	return Field{
		Name:        field,
		Value:       filepath.ToSlash(filepath.Join(filesDirName, id, storedName)),
		Kind:        "file",
		Filename:    filepath.Base(filename),
		ContentType: contentType,
		Size:        int64(len(data)),
		SHA256:      hex.EncodeToString(sum[:]),
	}, nil
}

// writeBlob atomically writes blob bytes, sealing them first when encryption at
// rest is enabled.
func (s *Store) writeBlob(path string, data []byte) error {
	sealed, err := s.encryptBlob(data)
	if err != nil {
		return err
	}
	return atomicWriteFile(path, sealed, 0644)
}

// ReadUpload returns the plaintext bytes of a stored blob. The stored name is
// re-validated so a crafted path cannot escape the blob directory.
func (s *Store) ReadUpload(table, id, storedName string) ([]byte, error) {
	dir, err := s.filesDir(table, id)
	if err != nil {
		return nil, err
	}
	name, err := safeStoredName(storedName)
	if err != nil {
		return nil, err
	}
	sealed, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	return s.decryptBlob(sealed)
}

// ReplaceUpload overwrites an existing blob in place and reports its new size
// and digest.
func (s *Store) ReplaceUpload(table, id, storedName string, data []byte) (int64, string, error) {
	if len(data) > maxUploadSize {
		return 0, "", fmt.Errorf("pod: upload exceeds %d bytes", maxUploadSize)
	}
	dir, err := s.filesDir(table, id)
	if err != nil {
		return 0, "", err
	}
	name, err := safeStoredName(storedName)
	if err != nil {
		return 0, "", err
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return 0, "", ErrNotFound
		}
		return 0, "", err
	}
	if err := s.writeBlob(path, data); err != nil {
		return 0, "", err
	}
	sum := sha256.Sum256(data)
	return int64(len(data)), hex.EncodeToString(sum[:]), nil
}

// DeleteUpload removes a single stored blob.
func (s *Store) DeleteUpload(table, id, storedName string) error {
	dir, err := s.filesDir(table, id)
	if err != nil {
		return err
	}
	name, err := safeStoredName(storedName)
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// deleteRecordFiles removes the whole blob directory for a record. It is called
// when a record is deleted and is a no-op when there are no uploads.
func (s *Store) deleteRecordFiles(table, id string) error {
	dir, err := s.filesDir(table, id)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	// Best effort: drop the now-empty per-table .pod-files directory so an
	// otherwise empty table does not keep a stray directory around.
	parent := filepath.Dir(dir)
	if entries, err := os.ReadDir(parent); err == nil && len(entries) == 0 {
		os.Remove(parent)
	}
	return nil
}

// FileField finds the record field that references the given stored blob name,
// so serving can recover the content type. It returns false when no field
// matches.
func (r *Record) FileField(storedName string) (Field, bool) {
	for _, f := range r.Fields {
		if !f.IsFile() {
			continue
		}
		if filepath.Base(filepath.FromSlash(f.Value)) == storedName {
			return f, true
		}
	}
	return Field{}, false
}

// sortUploads orders uploads deterministically (by field, then name, then
// digest) so a submission with several files produces stable, testable output.
func sortUploads(ups []fileUpload) {
	sort.SliceStable(ups, func(i, j int) bool {
		if ups[i].Field != ups[j].Field {
			return ups[i].Field < ups[j].Field
		}
		if ups[i].Filename != ups[j].Filename {
			return ups[i].Filename < ups[j].Filename
		}
		return string(ups[i].Data) < string(ups[j].Data)
	})
}

// readFilePart drains a multipart file part, enforcing the per-blob limit.
func readFilePart(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxUploadSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxUploadSize {
		return nil, fmt.Errorf("pod: upload exceeds %d bytes", maxUploadSize)
	}
	return data, nil
}

// --- HTTP routes ------------------------------------------------------------

// handleFiles serves {mount}/files/<table>/<id>/<name>. Because a table path
// may itself contain slashes, the last two segments are the record id and the
// stored blob name and everything before them is the table.
func (h *Handler) handleFiles(w http.ResponseWriter, r *http.Request, rest string) {
	segs := strings.Split(strings.Trim(rest, "/"), "/")
	if len(segs) < 3 {
		h.writeError(w, r, http.StatusBadRequest, "expected /files/<table>/<id>/<name>")
		return
	}
	name := segs[len(segs)-1]
	id := segs[len(segs)-2]
	table := strings.Join(segs[:len(segs)-2], "/")

	switch r.Method {
	case http.MethodGet:
		if !h.authorize(w, r, table, "read") {
			return
		}
		h.serveStoredFile(w, r, table, id, name)
	case http.MethodPut:
		if !h.authorize(w, r, table, "write") {
			return
		}
		h.replaceFile(w, r, table, id, name)
	case http.MethodDelete:
		if !h.authorize(w, r, table, "delete") {
			return
		}
		h.deleteFile(w, r, table, id, name)
	default:
		h.methodNotAllowed(w, r)
	}
}

// serveStoredFile streams a stored blob, recovering its content type from the
// referencing field. Values are decrypted transparently on the way out.
func (h *Handler) serveStoredFile(w http.ResponseWriter, r *http.Request, table, id, name string) {
	data, err := h.store.ReadUpload(table, id, name)
	if err != nil {
		if os.IsNotExist(err) {
			h.writeError(w, r, http.StatusNotFound, "file not found")
			return
		}
		h.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	ctype := "application/octet-stream"
	if rec, err := h.store.Get(table, id); err == nil {
		if f, ok := rec.FileField(name); ok && f.ContentType != "" {
			ctype = f.ContentType
		}
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Content-Disposition", "inline; filename="+strconv.Quote(name))
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// replaceFile implements PUT {mount}/files/<table>/<id>/<name>: the blob is
// overwritten in place and the record field's size/digest are refreshed.
func (h *Handler) replaceFile(w http.ResponseWriter, r *http.Request, table, id, name string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxUploadSize+1))
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if len(body) > maxUploadSize {
		h.writeError(w, r, http.StatusRequestEntityTooLarge, "upload too large")
		return
	}
	size, sum, err := h.store.ReplaceUpload(table, id, name, body)
	if err != nil {
		if err == ErrNotFound {
			h.writeError(w, r, http.StatusNotFound, "file not found")
			return
		}
		h.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if rec, err := h.store.Get(table, id); err == nil {
		if f, ok := rec.FileField(name); ok {
			f.Size = size
			f.SHA256 = sum
			if ct := r.Header.Get("Content-Type"); ct != "" {
				f.ContentType = ct
			}
			rec.SetField(f)
			rec.Version++
			_ = h.store.writeRecordFile(table, rec)
		}
	}
	switch h.format(r) {
	case "xml":
		h.writeXML(w, http.StatusOK, xmlMessage{Status: "ok", Message: "file replaced"})
	default:
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"status": "ok", "size": size, "sha256": sum,
		})
	}
}

// deleteFile implements DELETE {mount}/files/<table>/<id>/<name>: the blob is
// removed and its field dropped from the record.
func (h *Handler) deleteFile(w http.ResponseWriter, r *http.Request, table, id, name string) {
	if err := h.store.DeleteUpload(table, id, name); err != nil {
		if err == ErrNotFound {
			h.writeError(w, r, http.StatusNotFound, "file not found")
			return
		}
		h.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if rec, err := h.store.Get(table, id); err == nil {
		if f, ok := rec.FileField(name); ok {
			rec.DeleteField(f.Name)
			rec.Version++
			_ = h.store.writeRecordFile(table, rec)
		}
	}
	switch h.format(r) {
	case "xml":
		h.writeXML(w, http.StatusOK, xmlMessage{Status: "ok", Message: "file deleted"})
	default:
		h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "file deleted"})
	}
}
