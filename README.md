# pod — HTML-form database on the filesystem

**MIT License © Azzurro Technology Inc.** — standard library only, zero external
dependencies (`go.mod` is `require`-free).

pod is a database that is driven by HTML forms. A record is a single XML file
stored on disk inside a siloed directory, and the **destination URL of a form
submission maps directly to the storage location**:

```
POST /table/acme/contacts  →  stores <base>/acme/contacts/<id>.xml
```

<record>
  <field name="name">Jane</field>
  <field name="age">30</field>
</record>

Everything returns as **XML or JSON** (pick with `?format=json|xml` or Accept /
Content-Type negotiation), and a small vanilla HTML form UI is served for
browsers. There is no framework, no SQLite, no network database, no build
step — `go build ./...` and `go test ./...` are the whole toolchain.

## Modes

### 1. Standalone server

```sh
go run ./cmd/pod -port 8080 -db ./data
```

Flags:

| Flag | Default | Meaning |
|---|---|---|
| `-port` | `8080` | HTTP listen port |
| `-db` | `./data` | base directory for the filesystem database (silos) |
| `-mount` | `/` | URL prefix to serve under (use `/` standalone) |
| `-ui` | `true` | serve the embedded HTML form interface |
| `-index-interval` | `5s` | indexer sync interval (`0` disables) |
| `-normalize-interval` | `30s` | normalizer run interval (`0` disables) |
| `-version` | — | print `pod <version>` and exit |

### 2. Middleware mode (import the library)

```go
import "azzurrotech/pod"

st, _ := pod.Open("./data") // one shared index per base path
h := pod.NewHandler(pod.HandlerOptions{
    Store: st,
    Mount: "/pod",     // default "/pod"
    // UI defaults to enabled; pass pod.Ptr(false) to disable
})
http.Handle("/pod", h)
h.RunBackground(ctx, 5*time.Second, 30*time.Second) // indexer + normalizer
```

The same package also registers a Go `database/sql` driver:

```go
import "database/sql"
db, _ := sql.Open("pod", "./data")
```

## How data is stored

- One directory per table. Table paths may contain `/` to create silos
  (`acme/contacts`, `acme/invoices`). The URL destination path **is** the
  storage path — no mapping table.
- One XML file per record: `<base>/<table>/<id>.xml`.
- Writes are atomic (temp file + rename), so readers never see partial files.
- An optional schema lives at `<table>/.pod-schema.xml` and declares columns
  and defaults:

```xml
<schema table="acme/contacts" version="2">
  <column name="name" type="text"/>
  <column name="age" type="int" default="0"/>
</schema>
```

- Records keep `id`, `schema_version`, `version`, `created`, and `updated`
  metadata attributes.

## HTTP API

All responses are JSON by default; `?format=xml|json|html` or `Accept:`
headers switch the format. HTML form POSTs use the Post/Redirect/Get pattern
(303 → table page with a `notice`).

| Route | Methods | Purpose |
|---|---|---|
| `{mount}/` | GET | index page / table list |
| `{mount}/health` | GET | JSON health (versions, index, normalizer status) |
| `{mount}/tables` | GET / POST | list tables · create/update a schema |
| `{mount}/table/<path>` | GET / POST / PUT | query records (`?field=value` filters, `?q=` free text, `?limit=&offset=&orderby=&dir=`) · upsert a record (HTML form action) |
| `{mount}/record/<table>/<id>` | GET / PUT / POST / DELETE | fetch, update, delete one record (`_action=delete` on POST) |
| `{mount}/schema/<path>` | GET | table schema (JSON/XML) |
| `{mount}/sql?sql=…` | GET / POST | run SQL (form field, `?sql=`, or JSON `{"sql","args"}`) |
| `{mount}/normalize` | POST | run the normalizer now |
| `{mount}/reindex` | POST | run the indexer sync now |

### Request bodies

`application/x-www-form-urlencoded` and `multipart/form-data` (what HTML forms
produce), `application/json`, and `application/xml` (a `<record>` document) are
all accepted. JSON may be flat (`{"id":"1","name":"Jane"}`) or use the
response shape (`{"id":"1","fields":{"name":"Jane"}}`).

Reserved body/query keys: `id`, `_id`, `_action`, `format`, `limit`, `offset`,
`orderby`, `order`, `dir`, `q`, `sql`.

## SQL dialect (database/sql driver and `/sql` endpoint)

Same engine in both places. Supported:

- `CREATE TABLE t (col TYPE, col2 TYPE DEFAULT 'x')`, `DROP TABLE t`
- `INSERT INTO t (id, col) VALUES ('1', 'x')` — duplicate ids error
- `UPSERT INTO t (id, col) VALUES (..., ...)` — overwrite
- `SELECT cols|* FROM t WHERE … ORDER BY col ASC|DESC LIMIT n OFFSET n`
- `UPDATE t SET col = ? WHERE …`, `DELETE FROM t WHERE …`
- `?` placeholders; WHERE supports `= != <> > >= < <= LIKE NOT LIKE IS [NOT] NULL`
  and parentheses with AND/OR.

Transactions are **autocommit**: each statement commits on its own; `Commit` /
`Rollback` are no-ops — that is what single-writer filesystem semantics allow.

## Background services

- **Indexer** keeps a live in-memory index and record cache per base directory
  (`Index`), so equality queries narrow candidates instead of re-reading every
  file, and `SELECT`s avoid re-parsing unchanged XML. Writes update the index
  eagerly; the indexer sync catches out-of-band file edits (for example a
  backup restore). All stores opened on one base path share the index, so any
  number of `sql.Open("pod", base)` connections see each other's writes.
- **Normalizer** upgrades older record files to the current schema: when a
  schema gains a column with a default, records missing that field are
  backfilled (existing values are **never** overwritten).

## Current status and honest limitations

- Symlink-based sharing is **not implemented** yet.
- Per-value **encryption** of stored values is **not implemented** yet — values
  are stored as plain XML text.
- Values are strings; `int`/`float`/`bool`/`date` column types are advisory
  (comparisons are numeric-aware when values parse as numbers).
- The index is authoritative for equality narrowing only after a table has been
  scanned by the indexer; files written directly on disk are picked up on the
  next sync (or by hitting `/reindex`).
- No authentication or authorization is built in — pod serves whoever reaches
  the port. Put it behind your own auth layer (see SECURITY.md).

## Config example

```sh
pod-server -port 8080 -db /srv/pod/data -index-interval 5s -normalize-interval 30s
```

## Layout

```
doc.go, store.go      core store (filesystem XML database)
record.go, schema.go  XML record / schema types
cond.go               predicate/condition matcher (SQL WHERE engine)
index.go, indexer.go  shared in-memory index + sync service
normalizer.go         schema-default backfill service
statement.go          SQL tokenizer / parser / executor
driver.go             database/sql driver ("pod")
handler.go            HTTP handler (forms + JSON/XML API + UI)
ui/*.html             embedded vanilla HTML form interface
cmd/pod/              standalone server binary
legacy/current_gen/   previous(2025) JSON-REST generation (parked, own go.mod)
legacy/pod/           earlier XML + database/sql generations (parked reference)
```