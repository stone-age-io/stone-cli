# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`stone` is an opinionated Cobra-based CLI for the Stone Age IoT Platform. It talks to a PocketBase server for tenant data (things, locations, schemas, NATS users, Nebula hosts, etc.) and to NATS/JetStream for messaging and KV. Module path: `github.com/stone-age-io/stone-cli`. Go 1.25+.

## Build / dev

```sh
go build -o stone           # local binary (entry point: main.go -> cmd.Execute())
go build ./...              # build everything
go vet ./...
```

There is no test suite in the tree. The `version` in `cmd/root.go` is wired for `-ldflags` injection.

## Architecture

### Top-level layout
- `main.go` — one-liner that calls `cmd.Execute()`.
- `cmd/` — every Cobra command. Each file's `init()` attaches subcommands to `rootCmd` (no central registration).
- `internal/ctx` — on-disk config and named context model (XDG-based).
- `internal/pb` — thin PocketBase REST client + record (de)serialization.
- `internal/natsx` — NATS/JetStream connect helpers and the per-org nats-cli context sync logic.

### Persistent flags
Defined on `rootCmd` in `cmd/root.go`: `--context`, `--output` (`table|json|yaml`), `--debug`. Always construct PocketBase clients via `newPBClient(ctx)` so the `--debug` flag is honored (it logs requests/responses to stderr with a 4 KB body cap).

### Contexts (the central state object)
`internal/ctx/ctx.go` defines `Context` and `GlobalConfig`. State lives under `$XDG_CONFIG_HOME/stone/`:
```
stone/
├── config.yaml              # active_context, output
└── contexts/<name>/context.yaml
```
A context bundles: `url`, `auth` (PB token + collection + email), `current_organization`, `nats_url`, `nats_context` (name of the nats-cli context this CLI last wrote), and `workspace`. All commands resolve their working context through `ctx.Active(flagContext)`.

Context names must match `^[A-Za-z0-9_-]{1,50}$` — they're used as filesystem paths.

### Entity CRUD is data-driven
`cmd/entity.go` is the heart of typed CRUD. It declares 18 `EntitySpec` values (thing, location, location-type, thing-type, thing-type-operation, message-schema, organization, membership, invite, nats-user, nats-role, nats-import, nats-export, nats-account, nebula-network, nebula-host, nebula-ca, leaf-node). Each spec lists:
- `Collection` — PocketBase collection name
- `OrgScoped` — auto-inject `organization` on create / filter by it on `ls`
- `KeyColumns` — table columns shown by `ls`
- `Verbs` — empty = full `{ls, get, create, update, delete, edit}`; non-empty restricts (e.g. `nats-account`, `nebula-ca` are `{ls, get, update, edit}` only)
- `LookupKey` — record field accepted in place of an id by `get`/`update`/`delete`/`edit` (e.g. `code` for things/locations, `name` for most others, `hostname` for nebula-hosts; empty = id only, e.g. membership). Resolution lives in `resolveRecordID`: id-shaped args are tried as ids first, then fall back to an exact, org-scoped `LookupKey` match; 0 or >1 matches error out.
- `Fields` — typed flags: `FString | FInt | FBool | FJSON | FID | FIDs | FSelect | FMSelect`

`registerCRUD` synthesizes Cobra commands from each spec. **To add a new CRUD entity, add an `EntitySpec` — do not write per-entity command files.** `aliases()` auto-generates plural/underscore/hyphen variants so users can type `nats-users`, `nats_users`, `nats_user`, etc. interchangeably.

`get` and `ls` take `--fields` (comma-separated) for server-side projection via PocketBase's `fields` query param; on `ls` table output the requested fields become the columns.

Field types `FID`/`FIDs` accept 15-char PocketBase relation ids only. `FJSON` fields accept inline JSON, `@<path>`, or `-` (stdin).

### Fields the specs deliberately omit

`cmd/entity.go` is hand-maintained, not generated from the platform's
`schema.json`, so it drifts silently. When adding fields, these omissions are
intentional — do not "fix" them:

- **`organization`** — injected by `OrgScoped`, never a flag.
- **Server-generated material** — `public_key`, `private_key`, `seed`, `jwt`,
  `creds_file`, `certificate`, `config_yaml`, `signing_*`, `revocations`,
  `expires_at`. Readable via `get`; writing them is meaningless.
- **`nats-user.active`** — pb-nats reads it into its model
  (`internal/types/converters.go`) and consults it **nowhere** in JWT
  generation. Clearing it recolours a badge while the client keeps publishing;
  only `revoke` disconnects anyone. It stays a `KeyColumn` (pb-nats sets it when
  revoking) but must not become a writable flag. The console removed its
  equivalent checkbox for the same reason.
- **`nebula-ca.rotate_keys`** — no such field exists, in `schema.json` or in
  pb-nebula. It was a flag once and did nothing: PocketBase silently drops
  writes to fields a collection doesn't have, so it reported success every time.
- **`nats-account.rotate_keys` / `add_signing_key` / `remove_signing_key`** —
  the fields exist, but `nats_accounts.updateRule` is operator-only, so a tenant
  PATCH 404s. They live behind `stone nats account-keys` instead.
- **File fields** (`locations.floorplan`, `organizations.logo`) — no multipart
  upload path in the client.

Conversely, `things.active` and `leaf_nodes.active` **are** writable flags, and
they are not labels: the platform's `hooks/active_flag.go` treats the flip as a
decommission (refreshes `tokenKey` to kill live sessions, sets `revoke` on the
linked NATS identity). Help text must say so.

To re-check drift, diff the specs against the platform's `schema.json` — the
platform repo is normally at `../platform`.

### Custom platform routes

`internal/pb/client.go` has `CallRoute` for the platform's non-collection
endpoints, and `cmd/creds.go` wraps the two that exist:
`POST /api/me/nats-creds/rotate` and `POST /api/org/nats-account/keys`. Both
exist server-side because an API rule cannot express a single-field allowlist.
Neither takes a record id — the target is derived from the caller's identity or
active organization — so don't add one.

### Pull / apply (GitOps)
`cmd/sync.go`:
- `stone pull` writes one YAML file per record into `<workspace>/<collection>/<key>.yaml`, where `<key>` is the spec's `LookupKey` value (message-schemas use `ns__name__version`; fallback `name`, then id). Filename collisions get a `-<id>` suffix; records are pulled sorted by id so the suffix lands on the same record across pulls. Filenames are cosmetic — apply identifies records solely by the `id` field inside the file. Org-scoped collections are filtered by `current_organization`. Server-only fields (`collectionId`, `collectionName`, `created`, `updated`, `expand`) are stripped on read (see `pb.ServerOnlyFields` / `pb.Strip`).
- `stone apply` walks the workspace, infers each record's collection from its parent directory, batches up to **50 ops per request** (`batchSize` constant), and POSTs through PocketBase's transactional `/api/batch`. Records with `id` are PATCHed; records without are POSTed and the server-assigned id is written back into the file. Apply is idempotent. It deliberately does **not** delete records absent from the workspace.
- The list of collections pull/apply knows about is derived from `entitySpecs` (it reuses `OrgScoped` to inject `organization` on create). Adding an `EntitySpec` automatically extends both pull and apply.

### NATS context sync
`internal/natsx/sync.go` is the non-obvious one. When the stone context has `nats_url` set, `stone org switch <org>` (and `stone nats sync-context`):
1. Looks up the caller's `memberships` record for that org.
2. Reads the linked `nats_users` record's `creds_file` field.
3. Writes `<xdg.ConfigHome>/stone/creds/stone-<ctx>-<org>.creds` (the creds payload) and `<natsConfig>/nats/context/stone-<ctx>-<org>.json` (a nats-cli context JSON pointing at it).
4. Updates the stone context's `nats_context` field to the new name.

The matching shape lives in `natsCtxFile` and must stay compatible with both nats-cli and orbit.go's `natscontext` package.

**Two different config roots, deliberately** (`internal/natsx/paths.go`):
- stone's own state (contexts, creds) lives under `xdg.ConfigHome`, which follows platform convention — `%LOCALAPPDATA%` on Windows, `~/Library/Application Support` on macOS, `~/.config` on Linux.
- nats-cli context files must go where the nats tooling looks, which is `$XDG_CONFIG_HOME` or **`~/.config` on every OS** — nats-cli and orbit.go do not use platform conventions. Use `natsx.ContextDir()` / `ContextPath()` / `SelectedContextPath()`, never `xdg.ConfigHome`, for anything nats-cli must read. Writing contexts under `xdg.ConfigHome` is what silently broke sync on Windows and macOS: `sync-context` printed a path and success, and every later command died with `unknown context`. `SyncContextForOrg` removes an orphan left at the old location (only after confirming its description starts with `managed by stone`) and reports it as `RemovedPath`.

`natsx.Connect` resolves the context file itself and passes natscontext an **absolute path**, so the two cannot disagree about where contexts live. An unset or unresolvable `nats_context` is a hard error: falling through to the user's selected nats-cli context (natscontext's behavior for an empty name) connects to an unrelated server, or to `localhost:4222` when nothing is selected, and then looks exactly like a subscription that receives nothing. `--nats-context <name>` (persistent flag, applied in `cmd.natsConnect`) is the deliberate override. Every connection carries an `ErrorHandler`; without it nats.go swallows async server errors such as `Permissions Violation for Subscription to ...` — the other reason a sub sits silent. JetStream domain is honored automatically.

`org switch` always switches the org server-side; the NATS sync is a separate step that may print `nats-sync: skipped — <reason>` (no `nats_url`, no membership, no linked `nats_user`, or `--no-nats`). Don't conflate the two.

### NATS / JetStream commands
- `cmd/nats.go` — `pub` / `sub` / `request`, plus `natsConnect`, the helper every NATS-touching command dials through. `pub --js` routes through JetStream and prints the ack. `sub` flushes after subscribing and checks `sub.IsValid()` before printing `listening`, so a rejected subscription fails instead of hanging silently; it is a core subscription, so it never replays what a stream already holds (that's `js stream view`).
- `cmd/js.go` — stream/KV-bucket lifecycle admin, plus `js stream view` to read the last N stored messages (walks backward from `LastSeq` via `Stream.GetMsg`, tolerating purged-sequence gaps).
- `cmd/kv.go` — data-plane KV (`get`/`put`/`del`/`watch`/`ls keys`).
- Consumer management is intentionally absent — the `nats` CLI handles it better.

### PocketBase client conventions
`internal/pb/client.go` is intentionally small. Notable bits:
- `ListAll` pages with `PerPage=500` by default.
- `Batch` wraps `/api/batch` and returns one `BatchResponseItem` per op; surface per-op errors yourself.
- `DecodeJWTUserID` extracts `id` from the PB JWT without verifying — used after login to populate `Auth.UserID`.
- All HTTP errors funnel through `PBError`, which renders PocketBase's `data` map.

`internal/pb/serde.go` handles YAML/JSON marshalling for records and table output. `PrintList` / `PrintRecord` honor the global `--output` flag.

## Conventions when extending

- New tenant resource? Add an `EntitySpec` in `cmd/entity.go`. Pull/apply pick it up for free.
- New PocketBase endpoint? Extend `internal/pb/client.go`; don't sprinkle `net/http` into command files.
- New top-level command? Add a file under `cmd/` and wire it in that file's `init()` via `rootCmd.AddCommand(...)`.
- Org-scoped collections always need their `organization` field set. `ls` filters by it, `create` injects it, `apply` injects it for files missing an id. If you're working with an org-scoped collection by hand, set it explicitly or rely on those code paths.
- Positional record args (`get`/`update`/`delete`/`edit`) accept the spec's `LookupKey` value as well as an id. Relation flags (`FID`/`FIDs`) still require literal 15-char PB ids — there is no name-to-id lookup for flags; users discover ids via `stone <type> ls`.
