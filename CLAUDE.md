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
`cmd/entity.go` is the heart of typed CRUD. It declares 17 `EntitySpec` values (thing, location, location-type, thing-type, thing-type-operation, organization, membership, invite, nats-user, nats-role, nats-import, nats-export, nats-account, nebula-network, nebula-host, nebula-ca, activity). Each spec lists:
- `Collection` — PocketBase collection name
- `OrgScoped` — auto-inject `organization` on create / filter by it on `ls`
- `KeyColumns` — table columns shown by `ls`
- `Verbs` — empty = full `{ls, get, create, update, delete, edit}`; non-empty restricts (e.g. `nats-account`, `nebula-ca` are `{ls, get, update, edit}` only; `activity` is `{ls, get}`)
- `LookupKey` — record field accepted in place of an id by `get`/`update`/`delete`/`edit` (e.g. `code` for things/locations/organizations, `name` for most others, `hostname` for nebula-hosts; empty = id only, e.g. membership). Resolution lives in `resolveRecordID`: id-shaped args are tried as ids first, then fall back to an exact, org-scoped `LookupKey` match; 0 or >1 matches error out.
- `AltLookupKeys` — further fields accepted in place of an id, tried in order after `LookupKey`. Only `organization` has any (`name`, after `code`). They are tried ONE QUERY AT A TIME rather than OR-ed into a single filter, and that is the point: `organizations.code` and `organizations.name` are both unique columns, so a combined query could match two different records and force the CLI to refuse a lookup that is perfectly well defined. First key to match exactly one record wins, so a code beats a name.
- `Fields` — typed flags: `FString | FInt | FBool | FJSON | FID | FIDs | FSelect | FMSelect`
- `DefaultSort` — the PocketBase sort `ls` applies when `--sort` is absent (only `activity` sets one: a feed in insertion order is unreadable)

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
- **`nebula-ca.rotate`** — this one is real (pb-nebula v0.3.0), and is still not
  a flag: `nebula_ca.updateRule` is operator-only, so a tenant PATCH 404s. The
  three steps live behind `stone nebula ca-rotate`. Same for the material a
  rotation produces — `next_certificate`, `previous_certificate`, `rotated_at`.
  `routeOnlyFields` in `cmd/schema_drift_test.go` is what stops any of them
  becoming a flag later; it covers the `nats_accounts` triggers too, which were
  previously only prose.
- **`nats-account.rotate_keys` / `add_signing_key` / `remove_signing_key`** —
  the fields exist, but `nats_accounts.updateRule` is operator-only, so a tenant
  PATCH 404s. They live behind `stone nats account-keys` instead.
- **File fields** (`locations.floorplan`, `organizations.logo`) — no multipart
  upload path in the client.
- **Every field on `activity`** — the collection's three write rules are all
  nil, so nothing can write it through the API at all, tenant or operator. Its
  spec declares no `Fields` for that reason; the columns it does show are
  `KeyColumns`, and `actor` / `actor_type` / `resource_id` are left out of those
  on purpose (see `deliberatelyOmitted`) while remaining the right things to
  `--filter` on.

Conversely, `things.active` **is** a writable flag, and it is not a label: the
platform's `hooks/active_flag.go` treats the flip as a decommission (refreshes
`tokenKey` to kill live sessions, sets `revoke` on the linked NATS identity).
Help text must say so.

It is only meaningful on **update**. The same hook forces `active = true` on
every Thing create, because `things.authRule` is `active = true` and a
PocketBase bool has no schema default — an omitted field would land as false and
the device could never authenticate. So `thing create --active=false` is
silently overridden server-side; deactivation is an update. pb-nebula forces the
same thing on `nebula_hosts` create.

To re-check drift, refresh the vendored copy and run the tests — the platform
repo is normally at `../platform`:

```sh
cp ../platform/schema.json cmd/testdata/schema.json
go test ./...
```

`cmd/schema_drift_test.go` checks three directions per spec (a flag for a field
the platform lacks, a platform field neither exposed nor explained, and a stale
entry in either omission list) and one direction across the schema as a whole:
`TestEveryPlatformCollectionIsAccountedFor` fails on a collection the CLI models
nothing for. That fourth one exists because the other three all start from
`entitySpecs`, so a brand-new collection is one no test looks at — which is how
`activity` shipped in platform v0.8.0 and went unnoticed here through a schema
refresh. A new collection now needs either a spec or an entry in `notAnEntity`
saying why it has none.

### Custom platform routes
`internal/pb/client.go` has `CallRoute` (POST) and `GetRoute` (GET) for the
platform's non-collection endpoints. Five are wrapped:

- `cmd/creds.go` — `POST /api/me/nats-creds/rotate` and
  `POST /api/org/nats-account/keys`.
- `cmd/nebula.go` — `POST /api/org/nebula-ca/rotate` and
  `GET /api/org/nebula/cert-audit`.
- `cmd/thing.go` — `POST /api/org/things`, surfaced as `stone thing provision`.
- `cmd/invite.go` — `POST /api/org/invites/accept`, surfaced as
  `stone invite accept <token>`. It is a route because redeeming an invitation
  writes a `memberships` record on behalf of someone who is not yet in the
  organization, and every tenant rule is phrased in terms of a membership the
  caller does not have yet; the token is matched to the caller's own email
  address instead. Like the rest, it takes no record id.

Three of the first four exist because an API rule cannot express a single-field
allowlist: permitting one trigger field through an update rule means a deny-list
over every other, which silently opens the moment someone adds a field. None of
them takes a record id — the target is derived from the caller's identity or
active organization — so don't add one.

`cert-audit` is the exception and exists for a different reason: deciding whether
a host certificate still matches its network means parsing a Nebula certificate,
which no client can do. It answers in **ids**, and `cmd/nebula.go` resolves them
to hostnames before printing — falling back to the bare id rather than dropping
the row, because a host we cannot name is still a host that needs re-signing.

`POST /api/org/things` is there for a third reason: **atomicity**. Minting a
Thing with its NATS identity and its Nebula host used to be three unguarded
client calls, and a failure on the third orphaned a signed NATS credential and
an allocated overlay IP. The route runs all three in one PocketBase transaction —
and since pb-nats signs and publishes on `AfterCreateSuccess`, which PocketBase
defers to transaction completion, a rollback means NATS was never told. It also
needs two authority levels for one operation (a member may add inventory;
attaching an identity is owner/admin), which no create rule can express.

It is the only wrapped route that is **not** a replacement for a record write:
`stone thing create` still does a plain collection POST, because that is what
`apply` needs. `provision` is the one to reach for when standing up real
hardware. It attaches to the generated `thing` command tree through
`extraCommands` in `cmd/thing.go` — a package-level map `registerCRUD`
consults, so entity.go does not need to know what it is. `invite accept`
attaches the same way, and both **must** be entries in that map's literal:
package-level vars are initialized before any `init()`, but `init()`s run in
file-name order and entity.go's — the one that calls `registerCRUD` — sorts
first, so a command appended from `invite.go`'s or `thing.go`'s `init()` would
be attached to nothing, silently.

`/api/me/leaf-config` and `/api/client-config` are deliberately NOT wrapped: the
first requires a `things` session (an agent on the box asks for it, not an
operator at a terminal), and the second answers a question the browser has and
this CLI does not.

### pb-nebula v0.3 and the library-owned fields

pb-nebula v0.3.0 added twelve fields the platform's `schema.json` does not
declare — the dump predates the release. Unlike the pb-nats case noted in
`cmd/schema_drift_test.go`, pb-nebula **migrates** them (`addMissingFields`), so
a database created earlier acquires them on the next start rather than never.
They are listed in `libraryOwnedFields` so the drift guard can see them.

**The floor is pb-nebula v0.3.0** (the platform pins v0.3.2). Against v0.2.0 the
two Nebula routes 404, and `--is-relay`, `--unsafe-networks`, `--unsafe-routes`,
`--preferred-ranges`, `--mtu`, `--tun-device` and `--renew` all name fields the
collection does not have — so the write is discarded and the command reports
success. That is precisely the failure the drift guard exists to make loud, and
it cannot catch this one, because the vendored schema is a copy of a file that
never declared them either.

### Relation display (`cmd/relations.go`)

`ls` tables and `get`'s key/value output print a relation as the target's
natural key rather than a PocketBase id, via PocketBase's `expand` — same query,
no extra round trip. Seven specs had at least one column of raw ids before this.

The rules, all of which have a reason that is not obvious:

- **Human output only.** `-o json` / `-o yaml` never even send the `expand`
  parameter, so the scripting surface is byte-for-byte what the server returned.
  `edit` also fetches raw: what it opens in `$EDITOR` is PATCHed back, and a code
  written into a relation field would be sent as one. `pull` likewise — the
  workspace keys on ids.
- **No `Target` on `Field`.** An expanded record carries its own
  `collectionName`, so the label is looked up by that and uses the spec's
  `LookupKey`. One definition of "the natural key for this entity", and it cannot
  disagree with the platform about what a relation points at.
- **`organization` expands although no spec declares it**; it is injected from
  the active context, so it has no flag, but `get` still prints it.
- **`users` has no spec** and falls back to `email`, then `name` — PocketBase
  masks an auth record's email unless it is the caller's own, so a colleague
  shows as their name rather than a blank.
- **A `--fields` projection must also name `expand`** (`withExpandField`), because
  PocketBase applies `fields` to the whole body and `expand` is a top-level key
  like any other.
- **A blank cell means unset or unreadable**, deliberately conflated. Printing
  the id would defeat the column.

The behaviours above were verified against a running platform rather than
assumed, because every failure here is silent. Notably: a relation the caller
may not read is simply absent from `expand` and the request still returns 200 —
even with the target collection's `viewRule` set to null — so expansion cannot
break a command that would otherwise work, and there is no fallback path.


### Pull / apply (GitOps)
`cmd/sync.go`:
- `stone pull` writes one YAML file per record into `<workspace>/<collection>/<key>.yaml`, where `<key>` is the spec's `LookupKey` value, falling back to `name`, then id. Filename collisions get a `-<id>` suffix; records are pulled sorted by id so the suffix lands on the same record across pulls. Filenames are cosmetic — apply identifies records solely by the `id` field inside the file. Org-scoped collections are filtered by `current_organization`. Server-only fields (`collectionId`, `collectionName`, `created`, `updated`, `expand`) are stripped on read (see `pb.ServerOnlyFields` / `pb.Strip`).
- `stone apply` walks the workspace, infers each record's collection from its parent directory, batches up to **50 ops per request** (`batchSize` constant), and POSTs through PocketBase's transactional `/api/batch`. Records with `id` are PATCHed; records without are POSTed and the server-assigned id is written back into the file. Apply is idempotent. It deliberately does **not** delete records absent from the workspace.
- The list of collections pull/apply knows about is derived from `entitySpecs`, filtered by `EntitySpec.syncable()` — a spec offering neither `create` nor `update` is skipped by both, because every record it could write already has an id, so apply would PATCH it and a collection with no update rule answers 404. Adding a writable `EntitySpec` automatically extends both pull and apply; adding a read-only one (`activity`) deliberately extends neither. `OrgScoped` is reused to inject `organization` on create.
- **`pull` does not write credentials or server-generated material** — see `workspaceOmit` in `cmd/sync.go`. PocketBase already withholds the fields the schema marks hidden (every `private_key`, every `seed`), but it returns plenty to an owner or admin that has no business in a git repo: `nats_users.creds_file` **is** the credential (a user JWT and an nkey seed, the same bytes `natsx` writes under `0600`), `nebula_hosts.config_yaml` carries the host's Nebula private key inline, and `invites.token` is a bearer credential that redeems into a membership. Alongside them go the server-generated certificates, JWTs and action triggers, which are a correctness problem rather than a disclosure one: apply sends back every key in a file, so a pulled certificate is a stale value racing whatever the server has rotated to since. `pull` prints what it omitted, per collection. It is a **pull-side** filter only — a hand-written `revoke: true` still applies, so nothing here removes a capability. When adding a field to a spec, ask whether it belongs on this list too; `cmd/sync_test.go` pins the four whose absence is a credential leak.

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
- `ListAll` pages with `PerPage=500` by default. `ls --limit n` deliberately does not use it — it issues one `List` with `PerPage=n`, so the limit reaches the query rather than the printer.
- `Batch` wraps `/api/batch` and returns one `BatchResponseItem` per op; surface per-op errors yourself.
- `DecodeJWTUserID` extracts `id` from the PB JWT without verifying — used after login to populate `Auth.UserID`. `DecodeJWTExpiry` / `TokenExpired` read `exp` the same way.
- **`do` refuses an expired token before sending it**, and this is not belt-and-braces politeness — it is the only thing standing between a dead session and a wrong answer. PocketBase does **not** reject a token it will not accept: it serves the request as a guest, every list rule filters the result to nothing, and the response is `200` with an empty array. An aged-out context therefore printed empty tables, `no organizations visible to this user`, and `pulled 0 records` with exit status 0. The check reads the token's own `exp` claim rather than `Auth.Expires`, so it covers contexts written before the CLI recorded one; a token whose expiry cannot be parsed is sent as-is, because "unknown" is not "expired" and a claim rename should not be an outage.
- All HTTP errors funnel through `PBError`, which renders PocketBase's `data` map and appends a `stone auth login` hint on 401 — but not on 403, where the token is fine and the role is not.

`internal/pb/serde.go` handles YAML/JSON marshalling for records and table output. `PrintList` / `PrintRecord` honor the global `--output` flag.

## Conventions when extending

- New tenant resource? Add an `EntitySpec` in `cmd/entity.go`. Pull/apply pick it up for free.
- New PocketBase endpoint? Extend `internal/pb/client.go`; don't sprinkle `net/http` into command files.
- New top-level command? Add a file under `cmd/` and wire it in that file's `init()` via `rootCmd.AddCommand(...)`.
- Org-scoped collections always need their `organization` field set. `ls` filters by it, `create` injects it, `apply` injects it for files missing an id. If you're working with an org-scoped collection by hand, set it explicitly or rely on those code paths.
- Positional record args (`get`/`update`/`delete`/`edit`) accept the spec's `LookupKey` value as well as an id. Relation flags (`FID`/`FIDs`) still require literal 15-char PB ids — there is no name-to-id lookup for flags; users discover ids via `stone <type> ls`.
