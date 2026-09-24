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
package pod
