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
./stone org switch acme                          # by code, name, or id
./stone org switch acme --set-nats-default       # also point `nats` cli at it

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
  expires: 2026-10-19T03:54:32Z   # from the token's exp claim; informational
  email: admin@example.com
  user_id: abc123xyz0...
current_organization: orgID0000000001   # the id; commands display it as code (name) [id]
nats_context: stone-local      # nats-cli context to connect with; required for NATS commands
workspace: /home/me/my-workspace
```

### Sessions expire, and the server will not tell you

`stone` keeps its token on disk between invocations and nothing refreshes it.
When that token ages out, **PocketBase does not refuse it** — it serves the
request as a guest, every list rule filters the result to nothing, and the
answer is `200` with an empty array. Before this was handled, that looked like:

```
$ stone thing ls
ID  CODE  NAME  TYPE  LOCATION  ACTIVE
$ stone org ls
no organizations visible to this user
$ stone pull
pulled 0 records                            # exit status 0
```

An empty organization and a dead session are indistinguishable from the output.
So the CLI now refuses an expired token before sending it:

```
$ stone thing ls
error: the session for this context expired Thu, 27 Aug 2026 03:54:32 UTC — run: stone auth login
```

`stone auth whoami` reports the same thing without making a request, which is
the fastest way to answer "is it me or is it the server":

```
$ stone auth whoami
...
session:              valid until Fri, 16 Oct 2026 22:10:04 UTC
```

The check reads the token's own `exp` claim, so it works on contexts created
before the CLI recorded one. A token whose expiry cannot be parsed is sent
as-is — "unknown" is not "expired".

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

**Credentials are not written to the workspace.** PocketBase withholds the
fields the schema marks hidden — every `private_key` and `seed` — but it returns
plenty that is just as sensitive to an owner or admin, and `pull` drops those on
the way to disk:

| Collection | Not written | Why |
| :--- | :--- | :--- |
| `nats_users` | `creds_file`, `jwt`, `public_key`, `active`, `regenerate`, `revoke` | `creds_file` **is** the credential: a user JWT and an nkey seed, the same bytes `stone nats sync-context` writes under `0600` |
| `nebula_hosts` | `config_yaml`, `certificate`, `ca_certificate`, `expires_at`, `renew` | `config_yaml` carries the host's Nebula private key inline, and pb-nebula stores it in plaintext |
| `nebula_ca` | `certificate`, `expires_at`, `rotate`, `next_certificate`, `previous_certificate`, `rotated_at` | server-generated by a CA rotation |
| `nats_accounts` | `jwt`, `public_key`, `signing_public_key`, `signing_keys`, `active`, `revocations`, and the three key triggers | account key material and operator-only triggers |
| `invites` | `token`, `resend_invite` | the token is a bearer credential that redeems into a membership |

`pull` prints what it left out, per collection. The rest of each list is
server-generated state and action triggers, which do not belong in a
declarative file either: `apply` sends back every key in a file, so a pulled
certificate is a stale value racing whatever the server has rotated to since,
and a pulled trigger re-asserts an action that already happened.

This is a **pull-side** filter. A hand-written `revoke: true` still applies —
nothing here removes a capability, it only stops the CLI putting secrets on disk
unasked.

> **If you pulled with an earlier version**, those values are already in your
> workspace, and in its git history if you committed it. Treat them as
> disclosed: re-mint the NATS credentials (`stone nats-user update <username>
> --regenerate`, or `stone nats creds rotate` for your own) and re-issue the
> Nebula hosts (`stone nebula-host update <hostname> --renew`, which mints a
> fresh keypair as well as a certificate), then delete any outstanding invites
> whose token was written out. Scrubbing the files alone does not help once the
> values have been distributed.

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
- Admin: `organization`, `membership`, `invite`
- NATS: `nats-user`, `nats-role`, `nats-import`, `nats-export`
- Nebula: `nebula-network`, `nebula-host`

Limited CRUD (`ls / get / update / edit` only — auto-provisioned by the platform):

- `nats-account`, `nebula-ca`

Read-only (`ls / get`):

- `activity` — the organization's feed of who changed what. Append-only on the
  platform (all three write rules are nil), so there is nothing to create,
  update or delete. It lists newest-first without being asked, and it is
  excluded from `pull`/`apply`: a declarative workspace has nothing to say about
  a log of what already happened.

`edit` opens `$EDITOR` with the record as YAML and PATCHes on save.

### Lookup by id or natural key

`get`, `update`, `delete`, and `edit` accept either a 15-char PocketBase id or
the entity's natural key: `code` (`thing`, `location`, `location-type`,
`thing-type`, `organization`), `hostname` (`nebula-host`), `nats_username`
(`nats-user`), `email` (`invite`), and `name` for everything else. `membership`
is id-only. Key lookups are exact-match and scoped to the current organization;
zero or multiple matches fail with the candidate ids listed.

`organization` is the one entity with two human keys, and accepts **either**:
its `code` is tried first, then its `name`. Both are unique columns on the
platform, so the keys are tried one at a time rather than OR-ed into a single
query — an organization whose code is `acme` wins over a different one merely
*named* `acme`, deterministically, instead of the lookup being refused as
ambiguous.

`get` (alias `show`) and `ls` take `--fields` for server-side projection; on
`ls` table output the requested fields become the columns. `ls` also takes
`--limit`, which is a single page rather than a truncated fetch — without it,
`ls` pages through every matching record:

```sh
./stone thing get warehouse-hvac --fields code,name,location
./stone nebula-host delete edge-west
./stone thing ls --fields code,name
./stone activity ls --limit 20                          # newest 20, sorted for you
./stone activity ls --filter 'resource_id="<id>"'       # everything done to one record
```

### Relations read as codes, not ids

Table and `get` output print a relation as the target's natural key — the same
key you would type at `get`/`update`/`delete`:

```
$ stone thing ls
ID               CODE       NAME       TYPE         LOCATION  ACTIVE
sii1rtrxtadt2ad  sensor-42  Sensor 42  temp-sensor  hq        true
```

`type` and `location` are relations; before, both columns were 15-char ids.
PocketBase resolves them in the same query via `expand`, so this costs no extra
round trip.

**`-o json` and `-o yaml` are unchanged** — they are the scripting surface and
return exactly what the server sent, relation ids and all. The `expand`
parameter is not even added to those requests, so a pipeline that parses `type`
still gets an id, and `--fields id -o json` remains the way to discover one.
`edit` is also untouched: what it opens in `$EDITOR` is PATCHed back, and a code
in a relation field would be written as one.

The label is the target entity's `LookupKey`, chosen by the collection the
server names in the expanded record — so there is one definition of "the natural
key for this entity" rather than a second one per relation. `users` has no
entity of its own and falls back to `email`, then `name`: PocketBase masks an
auth record's email unless it is your own, so a colleague shows as their name.

A blank cell means the relation is unset **or** you cannot read the target.
Those are deliberately indistinguishable here; `-o json` shows the id either
way.

**Create and update still take ids.** Relation flags (`--type`, `--location`,
`--network-id`, …) are unchanged and want a 15-char PocketBase id. Reading is
where the ids were unreadable; writing is where they are unambiguous.

### Organizations are addressed by code

```
$ stone org ls
CURRENT  CODE           NAME            ID
*        acme           Acme Industries  r03ixjyfs4fbkp2
         warehouse-ops  Warehouse Ops    id5m2ymebgp4bkd

$ stone org switch warehouse-ops
switched to warehouse-ops (id5m2ymebgp4bkd)
```

The code is the platform's root identifier: globally unique, baked into signed
account JWTs and into the NATS subject namespace, and the value printed on a
sticker. It is what an `ORGANIZATION` relation column shows, and what
`org switch` and `organization get` take — the same rule as everywhere else
here, that the value you see is the value you may type.

The `name` still resolves, for `org switch "Warehouse Ops"` and
`organization get "Warehouse Ops"`. The id is still shown by `org ls` because
`membership create --organization` wants one.


### Auth-collection conveniences

`thing`, `nats-user`, and `nebula-host` are PocketBase auth collections. On create
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

### Provisioning a device in one transaction

`stone thing create` writes an inventory row and nothing else. For real
hardware, prefer:

```sh
./stone thing provision --code gw-01 --name "Gateway 01"     --type <thing_type_id> --location <location_id>     --nats-mode auto     --nebula-mode auto --nebula-network <id> --nebula-ip 10.128.0.42
```

This wraps the platform's `POST /api/org/things`, which creates the Thing, its
NATS identity and its Nebula host inside **one server-side transaction**. Doing
it as three separate writes is what the route exists to replace: a failure on the
third left a signed NATS credential and an allocated overlay IP owned by nothing.

Each identity takes a mode:

| Mode | Effect |
| :--- | :--- |
| `none` (default) | provision nothing |
| `auto` | mint a new one. NATS uses the org's active account and default role unless `--nats-role` names another; Nebula needs `--nebula-network` and `--nebula-ip` (no address is allocated for you) |
| `link` | attach an existing identity by id (`--nats-user`, `--nebula-host`) |

The email (`<code>@<org-code>.thing.local`) and password are generated
server-side, and the password is printed **once** — it is not retrievable
afterwards. Attaching either identity requires owner or admin; a member may
provision with both modes left at `none`.

`create` is still the right call for workspace-shaped work: `apply` writes
records, not devices.

### Joining an organization you were invited to

```sh
./stone invite accept <token>
./stone org switch warehouse-ops       # by code, name, or id; syncs the NATS context too
```

`accept` wraps the platform's `POST /api/org/invites/accept`. The token is the
`?token=` value from the invitation link, not the invite record's id, and the
invitation is matched to you by email address — so this only ever redeems one
issued to you.

Redeeming sets your `current_organization` **only if it was blank**: joining a
second organization does not move you out of the one you are working in. That is
why `org switch` is the next step rather than something `accept` does for you —
it is also what writes the nats-cli context for the new membership, which has no
creds on disk until it runs.

The other side of the flow is ordinary CRUD: `stone invite create --email … --role member`
issues one, `stone invite ls` shows what is outstanding, and
`stone invite update <email> --resend-invite` mails it again with a fresh token.

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
when hardware is retired or presumed lost. `--active=false` on a `thing`
signs it out immediately (its existing session token is invalidated,
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

## Nebula

The records are ordinary entities — `stone nebula-ca`, `stone nebula-network`,
`stone nebula-host`. Two operations are not record writes, and live under
`stone nebula`:

```sh
stone nebula cert-audit                 # hosts whose certificate no longer matches their network
stone nebula ca-rotate prepare          # publish trust in a new CA (reversible)
stone nebula ca-rotate commit           # switch issuance, re-sign every active host
stone nebula ca-rotate finish           # drop the outgoing CA
```

**`ca-rotate` takes three steps and the wait between them is the point.** Nebula
verification is mutual — each peer checks the other against its *own* local CA
pool, with no chain and no fallback — and hosts pull their config whenever they
like. So one write carrying both the new trust bundle and the new certificate
splits the mesh: a host that has fetched presents a new-CA certificate to one
that has not, and the handshake fails in *both* directions until propagation
finishes. `prepare` publishes trust and moves no issuance, so it is fully
reversible. `commit` switches issuance and re-signs every active host, with both
CAs trusted throughout. `finish` drops the outgoing CA and is refused while any
active host still holds a certificate signed by it — the refusal names the host.

A CA cannot be renewed, only rotated, so start months ahead of the expiry in
`stone nebula-ca ls`, not weeks.

**`cert-audit` answers a question no client can.** pb-nebula signed host
certificates at `/32` until v0.3.0. Nebula puts a certificate's network straight
onto the tun device and installs a link route for it, so the mask in the
certificate *is* the host's route to the overlay — a `/32` gives a host a route
covering only itself. The certificate verifies, the config renders, the host
starts, the handshake completes, and no packet crosses the mesh. Nothing errors,
which is why you have to ask. Editing a host's `overlay_ip` after issue lands it
here too.

Nothing is re-signed automatically: re-signing moves a fingerprint, and a
fingerprint is what the revocation blocklist matches, so a sweep would rewrite
every peer config in the mesh. Fix one host at a time, redeploying as you go:

```sh
stone nebula-host update edge-west --renew
```

Requires a platform on **pb-nebula v0.3.0 or newer**. Against v0.2.0 the routes
404 and the newer host flags — `--is-relay`, `--unsafe-networks`,
`--unsafe-routes`, `--preferred-ranges`, `--mtu`, `--tun-device`, `--renew` —
name fields the collection does not have, so PocketBase discards the write and
the command reports success.

## Limitations

- Relation flags (`--type`, `--location`, …) take 15-char PocketBase ids only —
  natural-key lookup applies to positional args, not to flags. Discover ids via
  `stone <type> ls` or `stone <type> get <key> --fields id`.
- Apply does not delete server records that are missing locally. Use the
  web UI or `stone <type> delete` for that.
- No JetStream consumer management — the `nats` CLI is better at that.
- `nats-account` and `nebula-ca` are **operator-only for every field**. Both
  `updateRule`s admit no tenant role, so an owner/admin PATCH of any field on
  either collection returns 404. The legitimate tenant operations live behind
  routes instead: `stone nats account-keys` and `stone nebula ca-rotate`.
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

They also fail on a whole collection the CLI does not model. That gap is how
`activity` shipped in platform v0.8.0 and went unnoticed here: every per-spec
check starts from `entitySpecs`, so a collection with no spec is a collection no
test looks at, and the feature is missing rather than broken. A new collection
now fails until someone writes a spec for it or adds it to `notAnEntity` with
the reason it has none.

When the platform's schema changes, refresh the copy and run the tests:

```sh
cp ../platform/schema.json cmd/testdata/schema.json
go test ./...
```

A failure then tells you exactly what moved. Add the field to the spec, or add it
to `deliberatelyOmitted` in `cmd/schema_drift_test.go` with the reason — "why is
there no flag for this" is the question that list exists to answer.
`cmd/testdata/schema-source.txt` records which platform version the copy came
from, on a `platform release:` line the release notes read back.

### Cutting a release

The release body comes from `CHANGELOG.md`, not from commit subjects — the
reasoning in this project lives in paragraphs, and the part an operator needs
(that a flag which used to report success was writing nothing) exists nowhere in
the commit log.

1. Promote `## [Unreleased]` to `## [X.Y.Z] - YYYY-MM-DD` and add the compare
   link at the foot of the file.
2. Refresh `cmd/testdata/schema-source.txt` if the platform moved, including its
   `platform release:` line.
3. Preview the body, which is worth reading before it is public:

   ```sh
   ./scripts/release-notes.sh v0.3.0
   ```

4. Tag and push. `.github/workflows/release.yml` builds the same body, hands it
   to goreleaser with `--release-notes`, and then **reads the published release
   back** to assert it is the one the script built.

That last step is not belt-and-braces. goreleaser exits 0 whether or not it used
the notes file, so a body that silently came out empty looks like a green run;
the platform shipped a release that way once. A tag pushed before `[Unreleased]`
was promoted fails at step 4's script instead, before anything is published.

The CLI versions independently of the platform. They are separate artifacts on
separate cadences, and a matching number would imply a compatibility contract
the code does not make — where a real constraint exists it is narrower than a
version (currently pb-nebula ≥ v0.3.0). The release notes state the platform
release each build was tested against instead.
