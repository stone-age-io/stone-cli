# stone

Opinionated CLI for the [Stone Age IoT Platform](https://github.com/stone-age-io).

`stone` complements the web console with command-line ergonomics for the things
the platform actually does: managing tenant resources (things, locations, and
the thing-type contract graph), publishing/subscribing to NATS, reading
JetStream KV, and pulling a tenant's configuration down as a folder of YAML you
can review, diff, and apply back from `git`.

Two names worth keeping straight: **`stone`** (this repo) is the client CLI you
run from a laptop or CI runner; **`stone-age`** is the platform server binary —
the Control Plane — that it talks to.

> **On "GitOps":** `stone apply` is a one-way, additive upsert, not a
> convergence loop. It creates and updates from files, never deletes, and does
> not detect drift until you `pull` again. Reviewable and reproducible, yes; a
> reconciler, no. See [Pull / apply](#pull--apply).

## Quickstart

```sh
# 1) Get the binary -- prebuilt, for the platform you are on
VERSION=0.1.0
curl -sSLO https://github.com/stone-age-io/stone-cli/releases/download/v${VERSION}/stone_${VERSION}_linux_amd64.tar.gz
tar xzf stone_${VERSION}_linux_amd64.tar.gz     # unpacks ./stone, LICENSE, README.md, SKILLS.md

#    linux, darwin and windows are all built, amd64 and arm64 each:
#    https://github.com/stone-age-io/stone-cli/releases/latest
#    Or build it yourself, which needs Go 1.25+:  go build -o stone

# 2) Create a context pointing at your platform server
./stone context create local \
    --url http://localhost:8090 \
    --nats-url nats://localhost:4222    # optional, enables per-org nats-context sync

# 3) Log in
./stone auth login          # prompts for email + password
./stone auth whoami

# 4) Pick an organization (mirrors users.current_organization on the server,
#    and writes a per-org nats-cli context if --nats-url is set)
./stone org ls
./stone org switch "System"
./stone org switch "System" --set-nats-default   # also point `nats` cli at it

# 5) Create resources with typed flags
./stone location create --name "HQ" --code hq
./stone thing-type create --name "Temp Sensor" --code temp-sensor \
    --subject-prefix "telemetry.sensors"
./stone thing create --email s42@example.com --code sensor-42 \
    --type <thing_type_id> --random-password        # password printed to stderr
./stone thing get sensor-42 --fields code,name,location   # read back by code or id

# 6) Or use the declarative workflow
mkdir my-workspace && cd my-workspace && git init
../stone pull --set-workspace .          # writes <collection>/<code>.yaml
# ...edit any file, commit, then:
../stone apply

# 7) NATS
./stone nats pub demo.hello 'world'
./stone nats pub demo.hello '@msg.json' --js     # JetStream publish
./stone nats sub 'demo.>'
./stone kv put twins device.42 '{"online":true}'
./stone kv watch twins
```

## Configuration

Per-user config lives under `$XDG_CONFIG_HOME/stone/`
(`~/.config/stone/` on macOS/Linux, `%APPDATA%\stone\` on Windows):

```
stone/
├── config.yaml             # active_context, output defaults
└── contexts/
    └── local/
        └── context.yaml    # url, auth token, current org, nats context, workspace
```

A `context.yaml`:

```yaml
name: local
url: http://localhost:8090
auth:
  collection: users
  token: eyJ...
  email: admin@example.com
  user_id: abc123xyz0...
current_organization: orgID0000000001
nats_context: stone-local      # nats-cli context to connect with; required for NATS commands
workspace: /home/me/my-workspace
```

## NATS

`stone` reuses the user's existing `nats` cli contexts via
[orbit.go's natscontext](https://github.com/synadia-io/orbit.go) module.

There are two ways to wire it up:

1. **Per-org sync (recommended).** Set `nats_url` on the stone context once
   (via `--nats-url` on `context create` or `org switch`). Then `stone org
   switch <org>` looks up your membership, reads the linked `nats_user`'s
   `creds_file`, writes a fresh creds file under stone's config dir, and writes
   a matching `~/.config/nats/context/stone-<ctx>-<org>.json`.
   The stone context's `nats_context` field is updated to point at it.
   Pass `--set-nats-default` to also update the nats-cli default context.

2. **Manual.** Set `nats_context` in the stone context yourself, pointing
   at any `nats context add` you've already created, or pass
   `--nats-context <name>` on a single command. JetStream domain (if any)
   is honored automatically.

Use `stone nats sync-context` to re-issue the context after rotating keys
(e.g., after `stone nats-user update <id> --regenerate`).

`stone context show` prints where it expects the context file and flags it as
`MISSING` when it isn't there.

**Context files always live in `~/.config/nats/context/`** (or
`$XDG_CONFIG_HOME/nats/context/`), on Windows and macOS too — that's where the
`nats` cli itself reads them, so the two tools stay interchangeable. If you
upgraded from a version that wrote them elsewhere, `sync-context` moves the file
and tells you what it removed.

If `nats_context` is unset or its file is missing, NATS commands stop with an
error naming the path. They deliberately do **not** fall back to whatever
`nats context select` points at: that fallback connects to an unrelated server
and shows up as a subscription that never receives anything.

### When nats-sync skips

`stone org switch` always switches the org; the NATS sync is a separate
step that can short-circuit. When it does, the output line starts with
`nats-sync: skipped — <reason>`. Reasons in plain English:

- `no NATS URL on this stone context` — set with `--nats-url nats://host:4222`
  on `org switch` or `context create`. Persists once.
- `no membership found for this user+org` — you're acting as an operator
  on an org you aren't a member of. NATS creds are per-membership.
- `membership has no linked nats_user` — the platform's hooks haven't
  provisioned a NATS user for this membership yet.
- `(--no-nats)` — you passed the flag.

Pass `--verbose` to either `stone org switch` or `stone nats sync-context`
to see the user id, membership id, NATS user id, and `creds_file` length
on stderr.

### When `stone nats sub` shows nothing

`sub` prints the server it connected to, so start there — if that URL isn't the
one you expected, your `nats_context` points at the wrong context. Otherwise:

- **The messages are already stored, not still flowing.** `sub` is a core NATS
  subscription: it only shows what is published from now on. To read what a
  stream already holds, use `stone js stream view <stream>`.
- **Your creds can't read that subject.** The server's reply
  (`Permissions Violation for Subscription to ...`) is printed to stderr and
  the command exits; check the `nats_role` on your `nats_user`.
- **Nothing is publishing.** Confirm with `stone js stream ls` — a stream whose
  `MESSAGES` count is climbing has live traffic.

## Pull / apply

`stone pull` writes one YAML file per record into `<workspace>/<collection>/`,
named by the record's natural key — the same keys CRUD lookup uses (`code`,
`name`, `hostname`, …), falling back to `name`, then id. Filename collisions get a `-<id>` suffix. Filenames
are cosmetic: `apply` identifies records solely by the `id` field inside each
file. Org-scoped collections are filtered to the current organization.

`stone apply` walks the workspace (or the paths you pass), groups records into
batches of up to 50, and POSTs them through PocketBase's transactional `/api/batch`
endpoint. Records with an `id` are PATCHed; records without are POSTed and the
returned id is written back into the file. Apply is safe to re-run.

For diff/status/history, put the workspace in `git`.

**What you get and what you don't.** Apply creates and updates; it never
deletes, and it has no control loop. So the workspace is a snapshot from your
last `pull` plus your edits — not a live mirror of the server. A record someone
created in the console won't be in your workspace until you pull again, and two
people applying different edits to the same record will not conflict: the last
apply wins, field by field, silently. If a workspace is shared, `pull` before
`apply` the way you'd `git pull` before pushing.

## Entities supported by typed CRUD

Full CRUD (`ls / get / create / update / delete / edit`):

- Domain: `thing`, `location`, `location-type`, `thing-type`, `thing-type-operation`
- Edge: `leaf-node`
- Admin: `organization`, `membership`, `invite`
- NATS: `nats-user`, `nats-role`, `nats-import`, `nats-export`
- Nebula: `nebula-network`, `nebula-host`

Limited CRUD (`ls / get / update / edit` only — auto-provisioned by the platform):

- `nats-account`, `nebula-ca`

`edit` opens `$EDITOR` with the record as YAML and PATCHes on save.

### Lookup by id or natural key

`get`, `update`, `delete`, and `edit` accept either a 15-char PocketBase id or
the entity's natural key: `code` (`thing`, `location`, `location-type`, `thing-type`, `leaf-node`),
`hostname` (`nebula-host`), `nats_username` (`nats-user`), `email` (`invite`),
and `name` for everything else. `membership` is id-only. Key lookups are
exact-match and scoped to the current organization; zero or multiple matches
fail with the candidate ids listed.

`get` (alias `show`) and `ls` take `--fields` for server-side projection; on
`ls` table output the requested fields become the columns:

```sh
./stone thing get warehouse-hvac --fields code,name,location
./stone nebula-host delete edge-west
./stone thing ls --fields code,name
```

### Auth-collection conveniences

`thing`, `nats-user`, `nebula-host`, and `leaf-node` are PocketBase auth collections. On create
and on password change, PB requires `passwordConfirm` to match `password` and
`emailVisibility` to be set explicitly. `stone` fills both in for you when a
non-empty `password` is present (typed CRUD, `apply`, and `edit` all benefit).

For non-interactive flows, pass `--random-password` to `create` instead of
`--password`. The CLI generates a 32-char URL-safe password via `crypto/rand`
and prints it once to **stderr**, so stdout stays clean for `jq`:

```sh
./stone thing create --email reader-01@things.example.com --code reader-01 \
    --type <thing_type_id> --random-password -o json 2> reader-01.pw
```

`--password` and `--random-password` are mutually exclusive; exactly one must be
passed.

## Credential lifecycle

Four distinct operations, easy to confuse. They are not interchangeable.

| Goal | Command |
| :--- | :--- |
| Replace **my own** credential | `stone nats creds rotate` |
| Replace **someone else's** credential | `stone nats-user update <username> --regenerate` |
| **Kill** a credential (compromise) | `stone nats-user update <username> --revoke` |
| **Decommission the device** | `stone thing update <code> --active=false` |

**Rotation is not revocation.** Regenerating issues a new credential and leaves
the old one working until it expires. After a suspected compromise, `--revoke`
is the one that bites: it adds the public key to the account's revocation list
and re-signs the account JWT, so NATS rejects the old credential immediately and
permanently. Re-enable with `--regenerate`, which mints a JWT with a later issue
time; the revoked one stays dead.

> **There is deliberately no `--active` flag on `nats-user`.** `pb-nats` reads
> that field into its model and then consults it nowhere in JWT generation — so
> clearing it turns a status badge red while the client keeps publishing. It
> remains readable as a status column (pb-nats sets it itself when revoking),
> but it is not a control. Use `--revoke`.

Deactivating the **device** is the broadest of the four and the one to reach for
when hardware is retired or presumed lost. `--active=false` on a `thing` or
`leaf-node` signs it out immediately (its existing session token is invalidated,
not just blocked at next login), stops it signing back in, and revokes its NATS
credential. Reactivating issues a *fresh* credential — the old `.creds` stays
revoked permanently, so the device must be given the new one.

Owner/admin only. Note this round-trips through `pull`/`apply`: a workspace file
carrying `active: false` decommissions real hardware on the next apply.

## Organization NATS account keys

```sh
stone nats account-keys add-signing              # routine rotation; existing user JWTs stay valid
stone nats account-keys remove-signing <pubkey>  # the last remaining key cannot be removed
stone nats account-keys rotate                   # EMERGENCY: purges all keys, invalidates every user JWT
```

Owner/admin, scoped to your active organization. These are routes rather than
record writes because `nats_accounts.updateRule` is operator-only — the record
mixes tenant-triggerable fields with the account limits and the signed account
JWT. The route takes no record id, so it cannot be aimed at another tenant.

Reach for `add-signing` for routine rotation. `rotate` is the response to a
suspected key compromise: every credential in the account must be re-minted
afterwards.

## JetStream streams (`stone js stream`)

```sh
./stone js stream ls
./stone js stream info <name>
./stone js stream view <name> --last 20                  # most recent N messages (newest first)
./stone js stream create twins --subject "twins.>" --max-age 24h --storage file
./stone js stream create twins --config stream.yaml      # advanced config
./stone js stream purge <name>
./stone js stream delete <name>
```

## KV (`stone kv`)

All KV operations — bucket lifecycle and per-key data — live under `stone kv`.

```sh
# Bucket lifecycle
./stone kv bucket ls
./stone kv bucket info <name>
./stone kv bucket create twins --history 5 --ttl 720h
./stone kv bucket delete <name>

# Data ops
./stone kv get twins device.42
./stone kv put twins device.42 '{"online":true}'
./stone kv put twins device.42 @./twin.json
./stone kv del twins device.42
./stone kv ls twins                                       # list keys in a bucket
./stone kv watch twins
```

## Limitations

- Relation flags (`--type`, `--location`, …) take 15-char PocketBase ids only —
  natural-key lookup applies to positional args, not to flags. Discover ids via
  `stone <type> ls` or `stone <type> get <key> --fields id`.
- Apply does not delete server records that are missing locally. Use the
  web UI or `stone <type> delete` for that.
- No JetStream consumer management — the `nats` CLI is better at that.
- `nats-account` and `nebula-ca` are **operator-only for every field**. Both
  `updateRule`s admit no tenant role, so an owner/admin PATCH of any field on
  either collection returns 404. The three legitimate tenant key operations live
  behind `stone nats account-keys` instead. `nebula_ca` has no rotation trigger
  at all — rolling a CA is an operator action.
- Locations' `floorplan` and organizations' `logo` are file fields; the CLI has
  no upload path for them. Use the console.

## AI assistant integration

`stone` ships a capability surface aimed at AI assistants that can shell out to
the CLI:

- [`SKILLS.md`](./SKILLS.md) — human-readable reference of what the CLI can do,
  bootstrap order, entity surface, NATS sync semantics, and known limitations.
- [`.claude/skills/stone/SKILL.md`](./.claude/skills/stone/SKILL.md) — imperative
  Claude Code skill auto-loaded when the user's request matches its trigger
  description (commands starting with `stone`, "create a thing", "switch org",
  "pull the workspace", etc.).

Both files describe the same surface but with different audiences. Update both
when you change command shapes that an assistant might rely on.

## Development

```sh
go build ./...
go vet ./...
go test ./...
```

Module path: `github.com/stone-age-io/stone-cli`. Go 1.25+.

### The entity table is checked against the platform's schema

The field table in `cmd/entity.go` is hand-maintained, not generated — so the
tests compare it against a **vendored copy** of the platform's collection
schema at `cmd/testdata/schema.json`. They fail in both directions: a flag for a
field the platform does not have (PocketBase discards that write and the command
reports success), and a platform field the CLI neither exposes nor records a
reason for.

When the platform's schema changes, refresh the copy and run the tests:

```sh
cp ../platform/schema.json cmd/testdata/schema.json
go test ./...
```

A failure then tells you exactly what moved. Add the field to the spec, or add it
to `deliberatelyOmitted` in `cmd/schema_drift_test.go` with the reason — "why is
there no flag for this" is the question that list exists to answer.
`cmd/testdata/schema-source.txt` records which platform version the copy came
from.
