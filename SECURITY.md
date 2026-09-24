# pod Security Overview

pod is a filesystem XML database driven by HTML forms. This document describes
what pod actually does today, what it deliberately does **not** do, and what
you must add when deploying it beyond a local/trusted context.

## What pod does

- **Path confinement.** Table paths and record ids are validated so they can
  never escape the configured base directory or traverse the filesystem
  (`..`, absolute paths, backslashes, and control characters are rejected).
- **No SQL injection.** SQL is parsed by pod's own tokenizer/parser with
  `?` placeholders; values never reach the filesystem through string-built SQL.
- **No raw-HTML injection in the UI.** The form interface is Go `html/template`,
  which escapes every stored value before rendering, so a record containing
  `<script>` is displayed as text, not executed.
- **XML is escaped on output.** Values are written as chardata with XML
  escaping; XML responses are marshalled by `encoding/xml`.
- **Atomic writes.** Record and schema files are written to a temp file and
  renamed, so readers never observe partial or torn writes.
- **Input size limits.** The HTTP server caps request bodies at 64 MiB and
  multipart at 4 MiB.

## What pod does NOT do (read this before deploying)

- **No authentication, no authorization, no rate limiting.** pod serves the
  whole database to anyone who can reach the port. Deploy behind an
  authenticating reverse proxy (e.g. Caddy) or mount the handler behind your
  own middleware (mode 2).
- **No encryption at rest.** Record values are plain XML text on disk. The
  planned per-value encryption feature is not implemented. If you need
  encryption today, use an encrypted volume or encrypt values before storing
  them.
- **No encryption in transit.** Use TLS at the proxy (Caddy/TLS terminator);
  pod itself serves plain HTTP.
- **No audit trails.** There is no access log beyond what your server/framework
  provides. Every mutation bumps a `version` and updates `updated` on the
  record file, which gives a light write history, but there is no per-action
  audit log.
- **Shared write access is not safe against concurrent writers.** Stores share
  one in-memory index per base directory; all writers go through that store,
  which serializes writes with a mutex. Two independent pod processes pointed
  at the same directory are NOT supported.
- **The in-memory index is a cache of the filesystem.** Out-of-band edits are
  picked up on the next indexer sync or `/reindex`. Until then, equality
  queries may not see them. If you edit files manually, reindex first.

## Security model summary

| Threat | Status |
|---|---|
| Path traversal / LFI | rejected (validated table paths + ids) |
| SQL injection | rejected (parameterized, own parser) |
| XSS via stored records | escaped (html/template) |
| XML injection / malformed records | rejected or escaped (encoding/xml, chardata) |
| Torn/partial writes | prevented (atomic rename) |
| Unauthorized access | **not handled — put auth in front** |
| Unencrypted storage | **accepted and documented; not implemented** |
| CSRF | not mitigated — pair with auth that is CSRF-resistant |

## Integration with ATP

pod is consumed by the AzzurroTech Platform (atp) as a submodule and will be
operated as one system with song and shepherd through atp's API surface. In
that configuration, authentication, secret handling, and a single exposed API
surface live in atp/shepherd, not in pod. Standalone pod deployments must
provide their own auth boundary.