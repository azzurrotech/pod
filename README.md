# pod — HTML forms → XML files, as Go middleware

**MIT License © Azzurro Technology Inc.** · module `azzurrotech/pod` ·
**Go standard library only** (`go.mod` is `require`-free; no third-party code).

pod is **middleware for a Go `http.FileServer`** (or any `http.Handler`). It lets
the static site you already serve accept HTML `<form>` submissions and persist
them as XML files, and turns a `GET` on a record into an HTML **form card
pre-filled with the stored values**. There is no database server, no SQLite, no
external dependency and no build step — `go build ./...` and `go test ./...`
are the whole toolchain.

The **destination URL of a form submission maps directly to the storage
location**:

```
POST /pod/table/acme/contacts   →   <base>/acme/contacts/<id>.xml
```

- **POST** interprets the submitted form controls and **creates or upserts** a
  record: pod matches the submission against records already in the system (by
  record `id`) and writes a new file when there is no match, or updates the
  existing file when there is.
- **GET** queries the data. A `GET` on a record renders a **pre-filled HTML form
  card** whose inputs carry the record's current values, ready to edit and
  resubmit.
- Every control documented by the
  [MDN Forms guide](https://developer.mozilla.org/en-US/docs/Learn_web_development/Extensions/Forms)
  is accepted (see [HTML form support](#html-form-support)).
- **No schema to declare and no migrations to run.** pod generates a table's
  schema from the form input it receives and grows it on the fly, resolving
  discrepancies gracefully (see [Schema](#schema--auto-generated-and-self-healing)).
- **Uploads, encryption, auth and sharing are first class.** `<input type="file">`
  uploads are persisted; encryption at rest is optional; an exposed auth helper
  wires in your own identity; and sharing uses links — never symlinks — so pod runs
  on filesystems without symlink support (see below).
- The same store is exposed as a **`database/sql` driver** so pod can be used
  as a SQL database from Go:
  [`sql.Open("pod", base)`](https://pkg.go.dev/database/sql).
- A **basic example UX** ships in the box using only raw, standard HTML, CSS and
  JavaScript (no framework, no CDN, no build step).

## Fitting pod next to a Go FileServer

Mount pod under a path and hand everything else to your `http.FileServer`:

```go
package main

import (
	"log"
	"net/http"

	"azzurrotech/pod"
)

func main() {
	st, err := pod.Open("./data") // one shared store/index per base directory
	if err != nil {
		log.Fatal(err)
	}

	p := pod.NewHandler(pod.HandlerOptions{
		Store: st,
		Mount: "/pod", // default; use "/" for standalone mode
	})

	mux := http.NewServeMux()
	mux.Handle("/pod/", p)                                 // forms → XML, JSON/XML API, example UI
	mux.Handle("/", http.FileServer(http.Dir("./site")))   // your static site

	log.Fatal(http.ListenAndServe(":8080", mux))
}
```

A form served by that FileServer then just points at pod:

```html
<form method="post" action="/pod/table/acme/contacts">
  <label>Name  <input name="name" required></label>
  <label>Email <input type="email" name="email"></label>
  <button type="submit">Save</button>
</form>
```

`POST /pod/table/acme/contacts` stores `<base>/acme/contacts/<id>.xml`; a
`GET /pod/record/acme/contacts/<id>` returns the pre-filled card (or XML/JSON,
chosen by `?format=` / `Accept`). The same handler also works standalone — see
[Running](#running).

`pod.NewHandler` only intercepts the paths under its mount; the FileServer keeps
serving your pages, stylesheets and scripts. To intercept only form traffic while
your own handler owns the rest, compose the two in a custom `http.Handler` and
delegate non-form requests to the FileServer.

## Request flow

### POST — create or upsert

`POST {mount}/table/<path>` (or `PUT`) reads the submitted controls and resolves
the target record:

| Situation | Result |
|---|---|
| no `id` / `_id` supplied | a new id is generated and a **new** record file is created |
| `id` supplied, no file matches | a **new** record file is created with that id |
| `id` supplied and a file matches | the matching record is **updated in place** (`version` is bumped, `updated` is refreshed) |
| `_action=delete` | the matching record file is deleted |

- Matching is on the record `id` (the primary key): `id` / `_id` form fields, or
  the id in a `PUT {mount}/record/<path>/<id>` path. To upsert by some other
  natural key, `GET {mount}/table/<path>?<field>=<value>` first to find the id.
- Every other named control becomes a `<field>`; existing fields not resubmitted
  are left untouched (upsert semantics).
- **The table schema is generated and widened from the submitted controls**, so a
  form may introduce new fields at any time without a migration or an error; see
  [Schema](#schema--auto-generated-and-self-healing).
- HTML clients get the Post/Redirect/Get pattern (303 → the table page with a
  `notice`); JSON/XML clients get a structured result.

### GET — query, and pre-filled form cards

| Request | Response |
|---|---|
| `GET {mount}/table/<path>` | query records (`?field=value` filters, `?q=` free text, `?limit=&offset=&orderby=&dir=`); HTML table, or JSON/XML |
| `GET {mount}/record/<path>/<id>` | one record; in HTML a **form card pre-filled with the correct input values** plus update/delete forms |
| `GET {mount}/schema/<path>` | the table schema |

## Stored format (XML — pod's choice)

pod defines the on-disk XML; the note above leaves the exact layout to pod. One
directory per table, one XML file per record at `<base>/<table>/<id>.xml`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<record id="1001" schema_version="1" version="2"
        created="2026-01-02T10:00:00Z" updated="2026-01-03T09:30:00Z">
  <field name="name">Jane</field>
  <field name="email">jane@example.com</field>
</record>
```

- **Table paths may contain `/`** to create silos (`acme/contacts`,
  `acme/invoices`); the URL path *is* the storage path — there is no mapping
  table.
- Values are written as XML chardata (escaped); the XML header is included.
- Records keep `id`, `schema_version`, `version`, `created` and `updated`
  metadata attributes.
- Writes are **atomic** (temp file + rename), so readers never see partial files.
- A schema is **generated from the record fields** on demand and can optionally be
  pinned at `<table>/.pod-schema.xml` (see below).
- Uploaded blobs live under `<table>/.pod-files/<id>/`, and share links are listed
  in `<base>/.pod-shares.xml` — all ordinary files, so no symlinks or special OS
  features are used (see [File uploads](#file-uploads) and
  [Sharing](#sharing--link-based-not-symlink-based)).

## Schema — auto-generated and self-healing

pod requires **no schema declaration and no migration step**. A table's schema is
**generated from the form input pod receives and modified as that input changes**,
so it can cover any request on the fly:

- **Generated on first write.** POSTing a form to a new table path creates the
  table there and derives the schema from the submitted controls — there is no
  separate "create table" prerequisite.
- **Modified on every write.** When a form submits a control that no record has
  used before, pod accepts it and appends it to the table's fields; the schema
  widens to fit the request instead of rejecting it.
- **Derived when absent.** With no explicit schema, the effective schema is the
  union of the field names already stored in the table (types are `text`).
- **Reconciled when present.** A pinned `<table>/.pod-schema.xml` is the declared
  shape; fields observed in submissions that it does not yet list are added to it,
  and its `version` is bumped so older records can be upgraded.
- **Discrepancies resolve gracefully, without errors.** Unknown/extra controls are
  stored, omitted controls leave their stored value untouched, missing columns fall
  back to their declared `default`, and values that do not match a column's advisory
  type are still stored as strings rather than failing the submission.
- **One source of truth.** `GET {mount}/schema/<path>` and the pre-filled form card
  both render the current effective schema, so the form always matches what pod
  will accept.

An explicit schema is an optional, versioned hint — never a gate:

```xml
<schema table="acme/contacts" version="2">
  <column name="name"  type="text"/>
  <column name="email" type="text" default=""/>
</schema>
```

Columns that gain a `default` are backfilled into pre-existing records by the
normalizer, which **never overwrites an existing value**.

## File uploads

`<input type="file">` works out of the box. A multipart POST stores each chosen
file next to the record and writes a *reference* into the record field, so
uploads travel with the record and can be served back over HTTP.

- **Where files go.** For record `id` in table `path`, blobs live under
  `<base>/<path>/.pod-files/<id>/`. Stored names are made unique and are never
  trusted verbatim — traversal (`..`, absolute paths, backslashes) is rejected.
- **What the record stores.** A file field keeps its metadata and points at the
  blob:

  ```xml
  <field name="avatar" kind="file" filename="cat.png"
         content_type="image/png" size="10240" sha256="…">
    .pod-files/1001/avatar-1-cat.png
  </field>
  ```

- **Multiple files.** A `multiple` input (or repeated field name) stores one
  reference per line, matching how other multi-valued controls are joined.
- **Serving, replacing, deleting.** `GET {mount}/files/<table>/<id>/<name>`
  streams the blob; re-uploading replaces it; deleting the record removes its
  `.pod-files` directory.
- **Graceful by default.** An empty file input is ignored (no file, no error); a
  submission carrying both text controls and files stores both.

## Encryption at rest (optional)

Encryption is **opt-in and off by default**. When enabled, values and uploaded
blobs are encrypted before they reach the disk, using the Go standard library
only — AES-256-GCM with a fresh random nonce per value.

- Enable with a 32-byte key: `pod.OpenEncrypted(base, key)` in middleware mode, or
  `-encrypt -key-file /path/to/key` (or the `POD_KEY` environment variable) for
  the standalone server.
- Encrypted values are tagged so plaintext and ciphertext can coexist within one
  table: `<field name="ssn" enc="aes-256-gcm">base64…</field>`. Reads decrypt
  transparently; a table does not have to be re-encrypted all at once.
- **Metadata stays plaintext** (`id`, `version`, timestamps, schema, filenames) so
  listing, paging and the form card keep working. Field *values* are opaque, so
  equality / `LIKE` filters cannot match encrypted columns server-side.
- Encryption is **at rest only**; terminate TLS in front for the wire.

## Sharing — link-based, not symlink-based

Sharing is done with **links, never filesystem symlinks**, so pod runs on
filesystems that have no symlink support (FAT/exFAT, restricted SMB/NFS mounts,
object storage, unprivileged Windows). A share is an opaque, unguessable token
that resolves through a normal index file to a record, a file, or a whole table:

- `POST {mount}/share` creates a link for a `table` / `record` / `file` and
  returns the URL; `GET {mount}/s/<token>` resolves it; `DELETE
  {mount}/share/<token>` revokes it.
- Tokens are scoped (read-only vs read/write), may carry an expiry, and are
  recorded in a share index (`<base>/.pod-shares.xml`). Because resolution is
  data-driven, nothing calls `os.Symlink` and no OS feature is required.
- The same link works anywhere the base directory is reachable — copy the data
  directory between machines and the links keep resolving.

## Authentication — an exposed helper

pod **exposes an auth helper** rather than hard-coding a scheme: your application
supplies identity, pod supplies the plumbing (middleware, sessions, CSRF, policy
hooks) around every route, including uploads and shares.

```go
auth := pod.NewAuth(pod.AuthOptions{
    // Your IdP, API keys, tokens — whatever you already use.
    Authenticate: func(r *http.Request) (pod.Principal, error) { … },
    // Optional: sessions/CSRF and a per-table, per-action policy.
    Policy: func(p pod.Principal, table, action string) bool { … },
})
mux.Handle("/pod/", auth.Wrap(p))
```

- The helper provides the request gate (`Wrap`), optional session-cookie handling,
  CSRF token issue/verify, and a `Policy` hook covering the `read` / `write` /
  `delete` / `share` actions.
- pod ships **no built-in login**. With no helper wired the server is **open**,
  which is fine for local/trusted use and explicit everywhere else.
- Because the helper is exposed, pod drops into any stack: implement
  `Authenticate` for your session store, SSO or token scheme and the same
  identity governs the HTML UI, the JSON/XML API, uploads and share links.

## HTML form support

pod's contract is to accept every control declared by the
[MDN Forms guide](https://developer.mozilla.org/en-US/docs/Learn_web_development/Extensions/Forms):
any element that carries a `name` is captured and stored as a `<field>`. HTTP
`GET` queries and `POST` upserts are both supported, and both
`application/x-www-form-urlencoded` and `multipart/form-data` enctypes are
parsed.

| MDN feature | How pod handles it |
|---|---|
| `<form method="get">` | `GET` — used to query records (filters, free text, paging, sorting) |
| `<form method="post">` | `POST` — create/upsert (or `_action=delete`) |
| `enctype=application/x-www-form-urlencoded` | parsed (`ParseForm`) |
| `enctype=multipart/form-data` | parsed (`ParseMultipartForm`, 4 MiB cap) |
| `<input>` text-ish types (`text`, `search`, `url`, `tel`, `email`, `password`, `hidden`) | stored verbatim as a string field |
| `<input>` date/time/number types (`date`, `month`, `week`, `time`, `datetime-local`, `number`, `range`, `color`) | stored verbatim as a string field (typed values are advisory) |
| `<input type="checkbox">` | checked boxes submit `name=value` (default `on`); stored as that field |
| `<input type="radio">` | the selected radio's value is stored under the shared group name |
| `<input type="file">` | the blob is persisted and the field stores a reference + metadata (see [File uploads](#file-uploads)) |
| `<textarea>` | text stored as a field |
| `<select>` / `<option>` / `<optgroup>` | selected value stored; `multiple` selections are joined with `\n` |
| `<button>` / `<input type="submit" name=…>` | the submitter's name/value is captured as a field |
| `<label>`, `<fieldset>`, `<legend>`, `<datalist>`, `<output>` | presentation/wiring only — not sent by the browser |
| `required`, `pattern`, `min`, `max`, `step`, `minlength`, `maxlength`, `type` validation | client-side HTML5 constraint validation; pod stores whatever is actually submitted |
| `disabled` controls | not submitted by the browser (per the HTML spec) |
| any custom control with a `name` | captured like any other field |

When several controls share a `name`, pod joins their values with a newline
(`\n`). Server-side validation lives in your application or schema defaults;
pod's own UI is rendered by Go `html/template`, which escapes every stored value.

## HTTP API

All responses are JSON by default; `?format=xml|json|html` or the request's
`Accept` / `Content-Type` headers switch the format. HTML form POSTs use the
Post/Redirect/Get pattern (303 → table page with a `notice`).

| Route | Methods | Purpose |
|---|---|---|
| `{mount}/` | GET | index page / table list (example UI) |
| `{mount}/health` | GET | JSON health (version, index, normalizer status) |
| `{mount}/tables` | GET / POST | list tables · create/update a schema |
| `{mount}/table/<path>` | GET / POST / PUT | query records (`?field=value`, `?q=`, `?limit=&offset=&orderby=&dir=`) · upsert a record (HTML form action) |
| `{mount}/record/<table>/<id>` | GET / PUT / POST / DELETE | fetch one record (HTML = pre-filled card) · update · `_action=delete` |
| `{mount}/schema/<path>` | GET | table schema (JSON/XML) |
| `{mount}/files/<table>/<id>/<name>` | GET / PUT / DELETE | download · replace · delete an uploaded file |
| `{mount}/share` | POST | create a share link for a table / record / file |
| `{mount}/s/<token>` | GET | resolve a share link |
| `{mount}/share/<token>` | DELETE | revoke a share link |
| `{mount}/sql?sql=…` | GET / POST | run SQL (form field, `?sql=`, or JSON `{"sql","args"}`) |
| `{mount}/normalize` | POST | run the normalizer now |
| `{mount}/reindex` | POST | run the indexer sync now |

### Request bodies

`application/x-www-form-urlencoded` and `multipart/form-data` (what HTML forms
produce), `application/json`, and `application/xml` (a `<record>` document) are
all accepted. JSON may be flat (`{"id":"1","name":"Jane"}`) or use the response
shape (`{"id":"1","fields":{"name":"Jane"}}`).

Reserved body/query keys: `id`, `_id`, `_action`, `format`, `limit`, `offset`,
`orderby`, `order`, `dir`, `q`, `sql`.

## database/sql driver

pod registers a `database/sql` driver named `"pod"`; the data source name is the
store's base directory (created if missing). The driver satisfies the
`database/sql/driver` interfaces (`Driver`, `Conn`, `Stmt`, `Rows`, `Tx`,
`Result`) and is registered in `init()`:

```go
import (
	"database/sql"

	_ "azzurrotech/pod"
)

db, err := sql.Open("pod", "./data")
if err != nil {
	log.Fatal(err)
}
defer db.Close()

rows, err := db.Query("SELECT id, email FROM acme.contacts WHERE name = ?", "Jane")
```

Transactions are **autocommit**: each statement commits on its own; `Commit` and
`Rollback` are no-ops, which is what single-writer filesystem semantics allow.
`Rows` expose the record's fields, with `id` as the first column for `SELECT`.

## SQL dialect

One engine serves both the driver and the `{mount}/sql` endpoint. Supported:

- `CREATE TABLE t (col TYPE, col2 TYPE DEFAULT 'x')`, `DROP TABLE t`
- `INSERT INTO t (id, col) VALUES ('1', 'x')` — duplicate ids error
- `UPSERT INTO t (id, col) VALUES (..., ...)` — overwrite
- `SELECT cols|* FROM t WHERE … ORDER BY col ASC|DESC LIMIT n OFFSET n`
- `UPDATE t SET col = ? WHERE …`, `DELETE FROM t WHERE …`
- `?` placeholders; `WHERE` supports `= != <> > >= < <= LIKE NOT LIKE IS [NOT] NULL`
  and parentheses with AND/OR.

## Example UX

The bundled interface is a deliberately minimal example using only raw and
standard web platform pieces — semantic HTML, a little CSS, and a few lines of
vanilla JavaScript:

- `ui/index.html` — table list and a "create table" form (JS adds column rows).
- `ui/table.html` — filter/query form, record table with paging, and a "save a
  record" form.
- `ui/record.html` — the **pre-filled form card**: one input per schema column,
  plus update and delete forms.
- `ui/message.html` — notices and errors.

The templates are embedded with `//go:embed ui/*.html` and rendered with
`html/template`, so values are escaped automatically. JSON/XML clients skip the
UI entirely.

## Running

Standalone server (the example UI is served at `/`):

```sh
go run ./cmd/pod -port 8080 -db ./data
```

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `8080` | HTTP listen port |
| `-db` | `./data` | base directory for the filesystem database (silos) |
| `-mount` | `/` | URL prefix to serve under (use `/` standalone) |
| `-ui` | `true` | serve the embedded example form interface |
| `-index-interval` | `5s` | indexer sync interval (`0` disables) |
| `-normalize-interval` | `30s` | normalizer run interval (`0` disables) |
| `-encrypt` | `false` | encrypt values and blobs at rest (see [Encryption at rest](#encryption-at-rest-optional)) |
| `-key-file` | — | path to a 32-byte key file used when `-encrypt` is set |
| `-version` | — | print `pod <version>` and exit |

Options are also available as env vars when running behind a platform
(`ATP_*`); the binary itself reads flags. A `Dockerfile` builds the server as
`pod-server` and serves on port 8080 with `/app/data` as the volume:

```sh
pod -port 8080 -db /srv/pod/data -index-interval 5s -normalize-interval 30s
```

In middleware mode, start the same services from your own process:

```go
p.RunBackground(ctx, 5*time.Second, 30*time.Second) // indexer + normalizer
```

## Background services

- **Indexer** keeps a live in-memory index and record cache per base directory,
  so equality queries narrow candidates instead of re-reading every file, and
  `SELECT`s avoid re-parsing unchanged XML. Writes update the index eagerly; the
  indexer sync catches out-of-band file edits (for example a backup restore).
  All stores opened on one base path share the index, so any number of
  `sql.Open("pod", base)` connections see each other's writes.
- **Normalizer** upgrades older record files to the current schema: when a
  schema gains a column with a default, records missing that field are
  backfilled (existing values are **never** overwritten).

## Current status and honest limitations

- **No built-in login.** pod exposes the auth helper; if you do not wire it, the
  server is **open**. Put identity (and rate limiting, and TLS) in front for any
  untrusted network.
- **Encryption is opt-in and at rest only** — not in transit, and encrypted
  columns cannot participate in server-side equality / `LIKE` filters.
- **Share links are bearer capabilities.** Anyone holding a token has exactly the
  access it grants until it is revoked; protect the share-signing secret.
- **Uploads are capped by the request limit** (64 MiB body, 4 MiB multipart) and
  stored on the same filesystem as the records.
- Values are strings; `int`/`float`/`bool`/`date` column types are advisory
  (comparisons are numeric-aware when values parse as numbers).
- The index is authoritative for equality narrowing only after a table has been
  scanned by the indexer; files written directly on disk are picked up on the
  next sync (or by hitting `/reindex`).
- Two independent pod processes pointed at the same directory are **not**
  supported; all writers for a base path must go through one shared store.

Path confinement, input-size limits, atomic writes and escaping are covered in
the package documentation and are always on. pod parses SQL with its own tokenizer
and `?` placeholders (no string-built SQL), validates table paths and ids so they
cannot escape the base directory, and escapes all values on output.

## Layout

```
doc.go                    package documentation
store.go                  core store (filesystem XML database)
record.go, schema.go      XML record / schema types + atomic writes
cond.go                   predicate/condition matcher (WHERE engine)
index.go, indexer.go      shared in-memory index + sync service
normalizer.go             schema-default backfill service
statement.go              SQL tokenizer / parser / executor
driver.go                 database/sql driver ("pod")
files.go                  file-upload storage, metadata and serving
crypto.go                 optional AES-256-GCM encryption at rest
share.go                  link-based sharing (share index; no symlinks)
auth.go                   exposed auth helper (middleware, sessions, CSRF, policy)
handler.go                HTTP middleware (forms + JSON/XML API + example UI)
embed.go                  //go:embed ui/*.html
ui/*.html                 example interface (raw HTML/CSS/JS)
cmd/pod/main.go           standalone server binary
go.mod                    require-free (standard library only)
README.md                 this document
```

## Development

```sh
go build ./...   # library + cmd/pod
go test ./...    # unit + handler tests
go vet ./...
```

No code generation, no vendoring, no external modules.
