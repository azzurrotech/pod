package pod

// Optional encryption at rest.
//
// Encryption is opt-in and off by default. When a Store is opened with
// OpenEncrypted (or the standalone server is started with -encrypt plus a
// 32-byte key), field values and uploaded blobs are sealed before they reach
// the disk and opened transparently on read. Only the Go standard library is
// used: AES-256-GCM from crypto/aes + crypto/cipher with a fresh random nonce
// per value.
//
// What is encrypted: field *values* and file *blobs*.
// What stays plaintext (by design): record metadata (id, version, created,
// updated), the schema, field names and file-name metadata. This keeps
// listing, paging, the form card and the index working. Because values are
// opaque on disk, server-side equality / LIKE filters cannot match an
// encrypted column — the same caveat the README documents.
//
// The wire format of an encrypted string is
//
//	base64url_nopad(nonce || AES-256-GCM ciphertext)
//
// and an encrypted blob is stored as the raw bytes nonce||ciphertext. A value
// is tagged with enc="aes-256-gcm" in the record XML so plaintext and
// ciphertext can coexist within one table during an incremental migration.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

// encAlgorithm is the tag written to a Field's Enc attribute.
const encAlgorithm = "aes-256-gcm"

// encKeySize is the required key length in bytes (AES-256).
const encKeySize = 32

// cryptoCipher wraps an AES-256-GCM AEAD with fresh-nonce helpers.
type cryptoCipher struct {
	aead cipher.AEAD
}

// newCryptoCipher validates the key length and builds the AEAD. A key of the
// wrong length is a configuration error and is reported eagerly rather than at
// first write.
func newCryptoCipher(key []byte) (*cryptoCipher, error) {
	if len(key) != encKeySize {
		return nil, fmt.Errorf("pod: encryption key must be %d bytes, got %d", encKeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("pod: encryption key: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("pod: encryption AEAD: %w", err)
	}
	return &cryptoCipher{aead: aead}, nil
}

// sealBytes returns nonce||ciphertext for the given plaintext.
func (c *cryptoCipher) sealBytes(plain []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("pod: encryption nonce: %w", err)
	}
	out := make([]byte, 0, len(nonce)+len(plain)+c.aead.Overhead())
	out = append(out, nonce...)
	out = c.aead.Seal(out, nonce, plain, nil)
	return out, nil
}

// openBytes reverses sealBytes.
func (c *cryptoCipher) openBytes(sealed []byte) ([]byte, error) {
	ns := c.aead.NonceSize()
	if len(sealed) < ns {
		return nil, errors.New("pod: encrypted value is truncated")
	}
	plain, err := c.aead.Open(nil, sealed[:ns], sealed[ns:], nil)
	if err != nil {
		return nil, fmt.Errorf("pod: decryption failed (wrong key or corrupt value): %w", err)
	}
	return plain, nil
}

// sealString encrypts a string field value into the tagged base64 form.
func (c *cryptoCipher) sealString(plain string) (string, error) {
	raw, err := c.sealBytes([]byte(plain))
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// openString reverses sealString.
func (c *cryptoCipher) openString(enc string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("pod: encrypted value is not valid base64: %w", err)
	}
	plain, err := c.openBytes(raw)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// OpenEncrypted opens a store exactly like Open, then enables encryption at
// rest with the given 32-byte key. It returns an error when the key length is
// wrong, which lets callers fail fast at startup.
func OpenEncrypted(base string, key []byte) (*Store, error) {
	c, err := newCryptoCipher(key)
	if err != nil {
		return nil, err
	}
	s, err := Open(base)
	if err != nil {
		return nil, err
	}
	s.cipher = c
	return s, nil
}

// Encrypted reports whether the store encrypts values and blobs at rest.
func (s *Store) Encrypted() bool { return s != nil && s.cipher != nil }

// ParseEncryptionKey interprets raw key material from a key file or the
// POD_KEY environment variable. It accepts 32 raw bytes, a 64-character hex
// string, or base64 (standard or URL, padded or raw) that decodes to 32 bytes,
// and always returns exactly 32 bytes.
func ParseEncryptionKey(raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == encKeySize {
		return trimmed, nil
	}
	if decoded, err := hex.DecodeString(string(trimmed)); err == nil && len(decoded) == encKeySize {
		return decoded, nil
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if decoded, err := enc.DecodeString(string(trimmed)); err == nil && len(decoded) == encKeySize {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("pod: encryption key must be %d bytes (raw, hex or base64)", encKeySize)
}

// encryptRecord returns a shallow copy of rec in which every non-empty field
// value is sealed and tagged, leaving metadata fields untouched. The in-memory
// record is never mutated, so the index keeps serving plaintext.
func (s *Store) encryptRecord(rec *Record) (*Record, error) {
	if s.cipher == nil {
		return rec, nil
	}
	out := rec.Clone()
	for i := range out.Fields {
		f := &out.Fields[i]
		// An empty value is left empty: there is nothing secret to protect and
		// an empty string must round-trip as empty.
		if f.Value == "" {
			continue
		}
		enc, err := s.cipher.sealString(f.Value)
		if err != nil {
			return nil, err
		}
		f.Value = enc
		f.Enc = encAlgorithm
	}
	return out, nil
}

// decryptRecord opens every field tagged with a known cipher in place.
func (s *Store) decryptRecord(rec *Record) error {
	if s.cipher == nil {
		return nil
	}
	for i := range rec.Fields {
		f := &rec.Fields[i]
		if f.Enc == "" {
			continue
		}
		if f.Enc != encAlgorithm {
			return fmt.Errorf("pod: unknown at-rest cipher %q", f.Enc)
		}
		plain, err := s.cipher.openString(f.Value)
		if err != nil {
			return err
		}
		f.Value = plain
		f.Enc = ""
	}
	return nil
}

// encryptBlob seals raw file bytes for storage. When encryption is disabled it
// returns the input unchanged.
func (s *Store) encryptBlob(plain []byte) ([]byte, error) {
	if s.cipher == nil {
		return plain, nil
	}
	return s.cipher.sealBytes(plain)
}

// decryptBlob reverses encryptBlob.
func (s *Store) decryptBlob(sealed []byte) ([]byte, error) {
	if s.cipher == nil {
		return sealed, nil
	}
	return s.cipher.openBytes(sealed)
}
