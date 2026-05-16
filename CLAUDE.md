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
`cmd/entity.go` is the heart of typed CRUD. It declares ~14 `EntitySpec` values (thing, location, thing-type, message-schema, organization, membership, invite, nats-user, nats-role, nats-import, nats-export, nats-account, nebula-network, nebula-host, nebula-ca). Each spec lists:
- `Collection` — PocketBase collection name
- `OrgScoped` — auto-inject `organization` on create / filter by it on `ls`
- `KeyColumns` — table columns shown by `ls`
- `Verbs` — empty = full `{ls, create, update, delete, edit}`; non-empty restricts (e.g. `nats-account`, `nebula-ca` are `{ls, update, edit}` only)
- `Fields` — typed flags: `FString | FInt | FBool | FJSON | FID | FIDs | FSelect | FMSelect`

`registerCRUD` synthesizes Cobra commands from each spec. **To add a new CRUD entity, add an `EntitySpec` — do not write per-entity command files.** `aliases()` auto-generates plural/underscore/hyphen variants so users can type `nats-users`, `nats_users`, `nats_user`, etc. interchangeably.

Field types `FID`/`FIDs` accept 15-char PocketBase relation ids only. `FJSON` fields accept inline JSON, `@<path>`, or `-` (stdin).

### Pull / apply (GitOps)
`cmd/sync.go`:
- `stone pull` writes one YAML file per record into `<workspace>/<collection>/<code-or-id>.yaml`. Org-scoped collections are filtered by `current_organization`. Server-only fields (`collectionId`, `collectionName`, `created`, `updated`, `expand`) are stripped on read (see `pb.ServerOnlyFields` / `pb.Strip`).
- `stone apply` walks the workspace, infers each record's collection from its parent directory, batches up to **50 ops per request** (`batchSize` constant), and POSTs through PocketBase's transactional `/api/batch`. Records with `id` are PATCHed; records without are POSTed and the server-assigned id is written back into the file. Apply is idempotent. It deliberately does **not** delete records absent from the workspace.
- The list of collections pull/apply knows about is derived from `entitySpecs` (it reuses `OrgScoped` to inject `organization` on create). Adding an `EntitySpec` automatically extends both pull and apply.

### NATS context sync
`internal/natsx/sync.go` is the non-obvious one. When the stone context has `nats_url` set, `stone org switch <org>` (and `stone nats sync-context`):
1. Looks up the caller's `memberships` record for that org.
2. Reads the linked `nats_users` record's `creds_file` field.
3. Writes `~/.config/stone/creds/stone-<ctx>-<org>.creds` (the creds payload) and `~/.config/nats/context/stone-<ctx>-<org>.json` (a nats-cli context JSON pointing at it).
4. Updates the stone context's `nats_context` field to the new name.

The matching shape lives in `natsCtxFile` and must stay compatible with both nats-cli and orbit.go's `natscontext` package. Connections in `internal/natsx/connect.go` go through `natscontext.Connect(c.NATSContext, ...)` so the user's nats-cli contexts are reused — JetStream domain is honored automatically.

`org switch` always switches the org server-side; the NATS sync is a separate step that may print `nats-sync: skipped — <reason>` (no `nats_url`, no membership, no linked `nats_user`, or `--no-nats`). Don't conflate the two.

### NATS / JetStream commands
- `cmd/nats.go` — `pub` / `sub` / `request`. `pub --js` routes through JetStream and prints the ack.
- `cmd/js.go` — admin for streams and KV buckets (lifecycle only).
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
- Relation flags (`FID`/`FIDs`) require literal 15-char PB ids. There is no name-to-id lookup; document that users discover ids via `stone <type> ls`.
