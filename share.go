package pod

// Link-based sharing.
//
// Sharing is done with *links*, never with filesystem symlinks, so pod runs on
// filesystems that have no symlink support at all (FAT/exFAT, restricted
// SMB/NFS mounts, object storage, unprivileged Windows). A share is an opaque,
// unguessable token that resolves through a normal index file to a record, a
// file, or a whole table.
//
// The index lives at <base>/.pod-shares.xml and is an ordinary XML file:
//
//	<shares>
//	  <share token="…" kind="record" table="acme/contacts" id="1001"
//	         write="false" created="…" expires="…"/>
//	</shares>
//
// Because resolution is pure data (load the index, look up the token, dispatch
// to the normal route), nothing calls os.Symlink and no operating-system
// feature is required. Copy the base directory between machines and the links
// keep resolving.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// sharesFileName is the hidden index file at the root of the store.
const sharesFileName = ".pod-shares.xml"

// shareTokenBytes is the entropy of a share token before base64url encoding.
const shareTokenBytes = 32

// Valid share kinds.
const (
	ShareTable  = "table"
	ShareRecord = "record"
	ShareFile   = "file"
)

// Share is one entry in the share index.
type Share struct {
	XMLName xml.Name `xml:"share"`
	Token   string   `xml:"token,attr"`
	Kind    string   `xml:"kind,attr"`            // table | record | file
	Table   string   `xml:"table,attr"`           // target table path
	ID      string   `xml:"id,attr,omitempty"`    // record id (record/file)
	Name    string   `xml:"name,attr,omitempty"`  // stored blob name (file)
	Write   bool     `xml:"write,attr,omitempty"` // read/write capability
	Created string   `xml:"created,attr"`
	Expires string   `xml:"expires,attr,omitempty"`
}

// Expired reports whether the share's expiry (if any) has passed.
func (s Share) Expired(now time.Time) bool {
	if s.Expires == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, s.Expires)
	if err != nil {
		return true // an unparseable expiry is treated as already expired
	}
	return now.After(t)
}

// Valid reports whether the share has a well-formed kind and target.
func (s Share) Valid() error {
	switch s.Kind {
	case ShareTable:
		if s.Table == "" {
			return errors.New("pod: table share requires a table")
		}
	case ShareRecord:
		if s.Table == "" || s.ID == "" {
			return errors.New("pod: record share requires a table and an id")
		}
	case ShareFile:
		if s.Table == "" || s.ID == "" || s.Name == "" {
			return errors.New("pod: file share requires a table, id and name")
		}
	default:
		return fmt.Errorf("pod: unknown share kind %q", s.Kind)
	}
	return nil
}

// shareIndex is the XML document stored at <base>/.pod-shares.xml.
type shareIndex struct {
	XMLName xml.Name `xml:"shares"`
	Shares  []Share  `xml:"share"`
}

// sharesPath returns the absolute path of the share index.
func (s *Store) sharesPath() string { return filepath.Join(s.base, sharesFileName) }

// loadShares reads the share index, returning an empty index when absent.
func (s *Store) loadShares() (*shareIndex, error) {
	data, err := os.ReadFile(s.sharesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &shareIndex{}, nil
		}
		return nil, err
	}
	var ix shareIndex
	if err := xml.Unmarshal(data, &ix); err != nil {
		return nil, fmt.Errorf("pod: malformed share index: %w", err)
	}
	return &ix, nil
}

// saveShares writes the share index atomically.
func (s *Store) saveShares(ix *shareIndex) error {
	body, err := xml.MarshalIndent(ix, "", "  ")
	if err != nil {
		return err
	}
	out := append([]byte(xml.Header), body...)
	out = append(out, '\n')
	return atomicWriteFile(s.sharesPath(), out, 0644)
}

// newShareToken returns an opaque, unguessable token.
func newShareToken() (string, error) {
	b := make([]byte, shareTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("pod: share token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// CreateShare mints a share link for the target and records it in the index.
// ttl <= 0 means the link never expires. Expired entries are pruned at the same
// time so the index does not grow without bound.
func (s *Store) CreateShare(target Share, ttl time.Duration) (*Share, error) {
	if err := target.Valid(); err != nil {
		return nil, err
	}
	if _, err := s.TablePath(target.Table); err != nil {
		return nil, err
	}
	token, err := newShareToken()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	target.Token = token
	target.Created = now.Format(time.RFC3339)
	if ttl > 0 {
		target.Expires = now.Add(ttl).Format(time.RFC3339)
	}

	s.shareMu.Lock()
	defer s.shareMu.Unlock()
	ix, err := s.loadShares()
	if err != nil {
		return nil, err
	}
	kept := ix.Shares[:0]
	for _, sh := range ix.Shares {
		if !sh.Expired(now) {
			kept = append(kept, sh)
		}
	}
	ix.Shares = append(kept, target)
	if err := s.saveShares(ix); err != nil {
		return nil, err
	}
	return &target, nil
}

// ResolveShare looks up a token and returns the share it names. Expired tokens
// and unknown tokens both yield ErrNotFound.
func (s *Store) ResolveShare(token string) (*Share, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	s.shareMu.Lock()
	defer s.shareMu.Unlock()
	ix, err := s.loadShares()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for i := range ix.Shares {
		if ix.Shares[i].Token != token {
			continue
		}
		if ix.Shares[i].Expired(now) {
			return nil, ErrNotFound
		}
		sh := ix.Shares[i]
		return &sh, nil
	}
	return nil, ErrNotFound
}

// RevokeShare removes a token from the index. An absent token yields
// ErrNotFound.
func (s *Store) RevokeShare(token string) error {
	s.shareMu.Lock()
	defer s.shareMu.Unlock()
	ix, err := s.loadShares()
	if err != nil {
		return err
	}
	found := false
	kept := ix.Shares[:0]
	for _, sh := range ix.Shares {
		if sh.Token == token {
			found = true
			continue
		}
		kept = append(kept, sh)
	}
	if !found {
		return ErrNotFound
	}
	ix.Shares = kept
	return s.saveShares(ix)
}

// ListShares returns the active (non-expired) shares, ordered by creation time.
func (s *Store) ListShares() ([]Share, error) {
	s.shareMu.Lock()
	defer s.shareMu.Unlock()
	ix, err := s.loadShares()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := make([]Share, 0, len(ix.Shares))
	for _, sh := range ix.Shares {
		if !sh.Expired(now) {
			out = append(out, sh)
		}
	}
	return out, nil
}

// shareURL builds the public resolve URL for a token under the handler mount.
func (h *Handler) shareURL(token string) string {
	return h.url("/s/" + token)
}

// --- HTTP routes ------------------------------------------------------------

// shareResponse is the JSON/XML reply to POST {mount}/share.
type shareResponse struct {
	XMLName xml.Name `xml:"share" json:"-"`
	Token   string   `xml:"token,attr" json:"token"`
	URL     string   `xml:"url,attr,omitempty" json:"url"`
	Kind    string   `xml:"kind,attr" json:"kind"`
	Table   string   `xml:"table,attr" json:"table"`
	ID      string   `xml:"id,attr,omitempty" json:"id,omitempty"`
	Name    string   `xml:"name,attr,omitempty" json:"name,omitempty"`
	Write   bool     `xml:"write,attr" json:"write"`
	Expires string   `xml:"expires,attr,omitempty" json:"expires,omitempty"`
}

// handleCreateShare implements POST {mount}/share. Body fields: kind (or type),
// table, id, name, write (or mode=read|write), ttl (duration, e.g. "1h") or
// expires (RFC3339).
func (h *Handler) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	vals, _, err := h.parseRequest(r)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	kind := strings.ToLower(firstNonEmpty(vals["kind"], vals["type"]))
	if kind == "" {
		kind = ShareRecord
	}
	mode := strings.ToLower(vals["mode"])
	write := parseBool(vals["write"]) || mode == "write" || mode == "rw" || mode == "readwrite"
	sh := Share{
		Kind:  kind,
		Table: vals["table"],
		ID:    firstNonEmpty(vals["id"], vals["_id"]),
		Name:  vals["name"],
		Write: write,
	}
	if err := sh.Valid(); err != nil {
		h.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if !h.authorize(w, r, sh.Table, "share") {
		return
	}
	ttl, err := shareTTL(vals)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	created, err := h.store.CreateShare(sh, ttl)
	if err != nil {
		h.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	resp := shareResponse{
		Token: created.Token, URL: h.shareURL(created.Token), Kind: created.Kind,
		Table: created.Table, ID: created.ID, Name: created.Name,
		Write: created.Write, Expires: created.Expires,
	}
	switch h.format(r) {
	case "xml":
		h.writeXML(w, http.StatusOK, resp)
	case "html":
		h.render(w, r, "message.html", messageData{
			Mount: h.mount, Title: "Share link",
			Message: "Share link: " + resp.URL,
		})
	default:
		h.writeJSON(w, http.StatusOK, resp)
	}
}

// handleRevokeShare implements DELETE {mount}/share/<token>. Revocation needs
// the "share" action on the share's target table.
func (h *Handler) handleRevokeShare(w http.ResponseWriter, r *http.Request, token string) {
	if token == "" {
		h.writeError(w, r, http.StatusBadRequest, "missing token")
		return
	}
	sh, err := h.store.ResolveShare(token)
	if err != nil {
		h.writeError(w, r, http.StatusNotFound, "share not found or expired")
		return
	}
	if !h.authorize(w, r, sh.Table, "share") {
		return
	}
	if err := h.store.RevokeShare(token); err != nil {
		h.writeError(w, r, http.StatusNotFound, "share not found or expired")
		return
	}
	switch h.format(r) {
	case "xml":
		h.writeXML(w, http.StatusOK, xmlMessage{Status: "ok", Message: "revoked"})
	default:
		h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "revoked"})
	}
}

// handleResolveShare implements GET {mount}/s/<token>: it looks the token up in
// the share index and serves the target (table, record or file) through the
// same code paths as a normal request.
func (h *Handler) handleResolveShare(w http.ResponseWriter, r *http.Request, token string) {
	sh, err := h.store.ResolveShare(token)
	if err != nil {
		h.writeError(w, r, http.StatusNotFound, "share not found or expired")
		return
	}
	switch sh.Kind {
	case ShareTable:
		h.queryTable(w, r, sh.Table)
	case ShareRecord:
		h.serveRecord(w, r, sh.Table, sh.ID)
	case ShareFile:
		h.serveStoredFile(w, r, sh.Table, sh.ID, sh.Name)
	default:
		h.writeError(w, r, http.StatusInternalServerError, "invalid share")
	}
}

// shareTTL interprets the optional ttl/expires inputs. ttl may be a Go
// duration ("1h", "30m") or a whole number of seconds; expires is RFC3339.
func shareTTL(vals map[string]string) (time.Duration, error) {
	if exp := strings.TrimSpace(vals["expires"]); exp != "" {
		t, err := time.Parse(time.RFC3339, exp)
		if err != nil {
			return 0, fmt.Errorf("pod: invalid expires (want RFC3339): %v", err)
		}
		d := time.Until(t)
		if d <= 0 {
			return 0, errors.New("pod: expires is in the past")
		}
		return d, nil
	}
	raw := strings.TrimSpace(vals["ttl"])
	if raw == "" {
		return 0, nil
	}
	if d, err := time.ParseDuration(raw); err == nil {
		if d < 0 {
			return 0, errors.New("pod: ttl must not be negative")
		}
		return d, nil
	}
	if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
		return time.Duration(n) * time.Second, nil
	}
	return 0, fmt.Errorf("pod: invalid ttl %q", raw)
}

// firstNonEmpty returns the first non-empty argument.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// parseBool accepts the truthy spellings HTML forms and JSON commonly produce.
func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
