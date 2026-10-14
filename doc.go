// Package pod implements a filesystem-based database driven by HTML forms.
//
// Records are stored as XML files inside siloed directories. Each table is a
// directory on disk; each record is a single <record> XML file inside that
// directory. A table may be addressed through the URL that receives the HTML
// form submission: the destination path of the request maps directly to the
// directory where the record is stored.
//
// pod has no external dependencies: it is standard library only.
//
// # Two modes
//
// Server mode: build and run the included binary.
//
//	cmd/pod/pod -port 8080 -db ./data
//
// Middleware mode: import the package from another Go server and mount the
// handler on any prefix, or register the built-in database/sql driver.
//
//	h := pod.NewHandler(pod.HandlerOptions{Store: store, Mount: "/pod"})
//	http.Handle("/pod", h)
//
//	// or, as a database/sql driver (registered in init()):
//	db, _ := sql.Open("pod", "./data")
//
// # Background services
//
// Two background services run while the system is serving:
//
//   - Indexer: keeps a live in-memory index and record cache so queries do
//     not re-read the whole table on every request.
//   - Normalizer: compares record files against the current table schema and
//     backfills default values for fields that were recently added to the
//     schema, upgrading older XML files to the newer shape.
//
// Both services are started automatically by cmd/pod and can be run from
// middleware mode via Handler.RunBackground, or driven on demand with
// Store.NormalizeRecord / Indexer.Sync / Indexer.Rebuild.
//
// # Schema
//
// No schema has to be declared and no migrations are ever run. A table's
// schema is generated from the form input pod receives and modified as that
// input changes: a table with no pinned schema derives its columns from the
// records it already holds, and a pinned schema (<table>/.pod-schema.xml) is
// widened (and its version bumped) whenever a submission introduces a new
// field. Discrepancies resolve gracefully rather than erroring — unknown
// controls are stored, missing columns fall back to their default, and a
// malformed schema file is ignored in favour of the derived one.
//
// # File uploads
//
// <input type="file"> works out of the box: a multipart POST stores each blob
// under <table>/.pod-files/<id>/ and writes a reference (with file name,
// content type, size and SHA-256) into the record field. Blobs are served by
// GET {mount}/files/<table>/<id>/<name> and removed with the record.
//
// # Encryption at rest (optional)
//
// Encryption is opt-in: open the store with OpenEncrypted(base, key) (or run
// cmd/pod with -encrypt and -key-file/POD_KEY). Values and blobs are sealed
// with AES-256-GCM from the standard library; metadata stays plaintext so
// listing and the form card keep working. Reads decrypt transparently, so
// plaintext and ciphertext can coexist in one table.
//
// # Sharing
//
// Sharing is link-based, never symlink-based, so pod runs on filesystems
// without symlink support. POST {mount}/share mints an opaque token recorded
// in <base>/.pod-shares.xml; GET {mount}/s/<token> resolves it to a table,
// record or file, and DELETE {mount}/share/<token> revokes it.
//
// # Authentication
//
// pod ships no built-in login but exposes an auth helper: build one with
// NewAuth(AuthOptions{...}) and wrap the handler with auth.Wrap(h) to gate
// every route, optionally with in-memory sessions, CSRF and a per-table,
// per-action Policy. With no helper wired the server is open.
package pod
