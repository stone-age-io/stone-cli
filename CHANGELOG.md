# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) — with the pre-1.0
caveat that a minor version may break something. Pin what you deploy.

History before `0.1.0` is not reconstructed here; `git log` is the record for
that period, and this file starts where the versioned releases do.

## [Unreleased]

Nothing yet.

## [0.5.1] - 2026-09-22

**The one place 0.5.0 missed.** The first thing you see after `stone auth login`
is your current organization, and it was still a bare id. It is now the code,
with the name and id beside it. Nothing you type changes, and neither does
`-o json` / `-o yaml` output.

### Changed

- **The current organization prints as a code after login, too.** 0.5.0 gave
  `org ls`, `org switch` and relation columns the code, but `auth login`,
  `auth whoami` and `context show` still printed `current_organization` as a
  bare id. They now print `acme (Acme Industries) [r03ixjyfs4fbkp2]`.

  **`context.yaml` still stores the id**: it mirrors
  `users.current_organization` on the server and is what every org-scoped filter
  compares against, so this is display only. The label is a best-effort lookup
  with a 5-second limit — offline, or with an expired session, it falls back to
  the bare id rather than to nothing. `org current` is unchanged; its line keeps
  the id first for anything reading it by position.

- **`invite accept` names the organization you joined by code**, and its
  `next: stone org switch …` hint carries that code, so it can be pasted as-is.

## [0.5.0] - 2026-09-19

**Ids were for the machine; this release gives the human the codes back.** A
relation used to print as a 15-char PocketBase id — `stone thing ls` showed a
`TYPE` column of `pbc247972991`-shaped strings that nobody recognises. Table and
`get` output now print the target's natural key, and organizations are addressed
by their code throughout: in the column, at `org switch`, and at
`organization get`.

One rule connects the two changes, and it is worth stating because it also says
what is *not* in them: **the value you see is the value you may type.** So
`-o json` and `-o yaml` are untouched — they are the scripting surface and still
return exactly what the server sent, relation ids and all — and create/update
flags still take ids. Reading is where the ids were unreadable; writing is where
they are unambiguous.

### Changed

- **Organizations are addressed by code.** `org ls` gained a `CODE` column and a
  header, `org switch` takes a code, and an `ORGANIZATION` relation column shows
  one:

  ```
  $ stone org ls
  CURRENT  CODE           NAME             ID
  *        acme           Acme Industries  r03ixjyfs4fbkp2
           warehouse-ops  Warehouse Ops    id5m2ymebgp4bkd

  $ stone org switch warehouse-ops
  switched to warehouse-ops (id5m2ymebgp4bkd)
  ```

  The code is the platform's root identifier — globally unique, baked into
  signed account JWTs and into the NATS subject namespace (ADR 0002), printed on
  a sticker. Making it the organization's `LookupKey` is what makes the relation
  column show it, so this follows the same rule as the change above: the value
  you see is the value you may type.

  **`name` still resolves**, so `org switch "Warehouse Ops"` and
  `organization get "Warehouse Ops"` are unaffected. That needed a new
  `AltLookupKeys` on the spec, and keys are tried one query at a time rather than
  OR-ed into one filter — `organizations.code` and `organizations.name` are both
  unique columns, so a combined query could match two different records and
  force the CLI to refuse a lookup that is perfectly well defined. The code is
  tried first, so an organization *coded* `acme` deterministically beats a
  different one merely *named* `acme`.

  `org switch` now resolves through the same `resolveRecordID` every other
  positional lookup uses, instead of its own copy of the logic — which is how it
  gained id-fallback and the multi-match error message for free.

  Two smaller changes ride along. `org ls` prints a header and aligned columns
  like every other `ls` in the CLI, where it used to print a bespoke
  `* <id>  <name>`; `-o json` is unchanged. And `pull` names organization files
  by code now (`acme.yaml`, not `Acme-Industries.yaml`) — an existing workspace
  will gain the new file beside the old one on the next pull, and the stale one
  is safe to delete.

- **Relations read as codes instead of PocketBase ids.** `stone thing ls` showed
  `TYPE` and `LOCATION` as 15-char ids; it now shows `temp-sensor` and `hq`.
  Seven specs had at least one column of raw ids — things, locations,
  memberships, nats-users, nebula-networks, nebula-hosts and organizations —
  and a column of ids is a column nobody can read.

  PocketBase resolves it in the same query via `expand`, so this costs no extra
  round trip. The label is the target entity's `LookupKey` — the same key you
  would type at `get`/`update`/`delete` — chosen by the collection name the
  server puts in the expanded record, so there is one definition of "the natural
  key for this entity" rather than a second one written next to each relation.

  **`-o json` and `-o yaml` are unchanged.** They are the scripting surface and
  return byte-for-byte what the server sent; the `expand` parameter is not even
  added to those requests. `--fields id -o json` is still how you discover an id,
  and it is now also how you read a relation id back out. `edit` and `pull` are
  untouched for the same reason: what `edit` opens in `$EDITOR` is PATCHed back,
  and the workspace keys on ids.

  **Create and update still take ids.** Relation flags are unchanged. Reading is
  where the ids were unreadable; writing is where they are unambiguous.

  Two details worth knowing. `users` has no entity of its own, so a `user` or
  `owner` column falls back to `email`, then `name` — PocketBase masks an auth
  record's email unless it is your own, so a colleague shows as their name. And
  a blank relation cell means the relation is unset **or** you cannot read the
  target; `-o json` tells them apart.

  Every behaviour this relies on was checked against a running platform rather
  than assumed, because each failure here is silent. The one that decided the
  design: a relation the caller may not read is simply absent from the response
  and the request still returns 200 — even with the target collection's
  `viewRule` set to null — so expansion cannot break a command that would
  otherwise work, and the code carries no fallback path it would never take.


## [0.4.0] - 2026-09-19

**Two things that looked like success and were not.** `stone pull` wrote live
NATS and Nebula credentials into the workspace — the directory this project's
own docs tell you to commit to `git`. And an expired session was
indistinguishable from an empty organization: PocketBase serves a token it will
not accept as a *guest*, so every list came back empty with exit status 0.

**If you have ever run `stone pull`, this release asks something of you**: the
credentials it wrote are in your workspace and in its history. See **Fixed**
for what to re-mint. Upgrading stops new ones being written; it cannot unwrite
the old.

It also catches the CLI up to platform **v0.8.0** — `stone activity`, the tenant
feed of who changed what — and adds `stone invite accept`, which was the one
documented tenant route with no command behind it.

### Fixed

- **An expired session looked exactly like an empty organization.** PocketBase
  does not refuse a token it will not accept — it serves the request as a guest.
  Every list rule then filters the result down to nothing and the response is
  `200` with an empty array. So a context whose token had aged out printed a
  table header and no rows, `stone org ls` said *"no organizations visible to
  this user"*, `stone pull` reported `pulled 0 records` and exited zero, and
  nothing anywhere said the word "login". Found by running the new build against
  a real deployment: the context had been dead for three weeks and every command
  had been answering confidently.

  The CLI now refuses an expired token before sending it, naming the expiry and
  the command that fixes it. It reads the token's own `exp` claim rather than a
  stored timestamp, so contexts written before this change are covered too.
  `stone auth whoami` grew a `session:` line for the same reason — "am I still
  logged in" is the question it is asked, and it used to answer from local state
  that could not tell. `auth login` now records the expiry on the context as
  well, but nothing depends on that.

  A token whose expiry cannot be parsed is sent as-is. "Unknown" is not
  "expired", and refusing one this CLI could not read would turn a claim rename
  into an outage.

- **`stone pull` wrote live credentials into the workspace.** PocketBase
  withholds the fields the schema marks hidden — every `private_key` and `seed`
  — so the leak was not those. It was the material the API legitimately returns
  to an owner or admin, written to YAML in a directory the README tells you to
  put in `git`:

  - `nats_users.creds_file` **is** the credential. A user JWT and an nkey seed:
    the same bytes `stone nats sync-context` deliberately writes under `0600`,
    written here under `0644`, once per NATS identity in the organization.
  - `nebula_hosts.config_yaml` carries the host's Nebula private key inline.
    pb-nebula documents this itself — the standalone `private_key` column can be
    encrypted at rest and is hidden from the API, and the rendered config
    containing the same key is neither.
  - `invites.token` is a bearer credential that redeems into a membership.

  `pull` now drops those, along with the server-generated certificates, JWTs and
  action triggers on the same collections, and prints what it left out per
  collection. The full list is in the README.

  This is a **pull-side** filter only: a hand-written `revoke: true` still
  applies, so nothing here removes a capability.

  **If you have pulled before, the values are already in your workspace and in
  its git history.** Treat them as disclosed — re-mint with
  `stone nats-user update <username> --regenerate` and
  `stone nebula-host update <hostname> --renew` (which mints a fresh keypair,
  not just a certificate), and delete any invitation whose token was written
  out.

  The server-generated half was a correctness bug as well as a disclosure one:
  `apply` sends back every key in a file, so a pulled certificate is a stale
  value racing whatever the server has rotated to since.

### Added

- **`stone activity`** (`ls` / `get`) — the platform's tenant activity feed,
  added in platform v0.8.0 and missing here since. Actor, action, record and
  timestamp, scoped to your organization and readable by every role.

  Read-only because it is read-only everywhere: all three write rules on the
  collection are nil, so it cannot be forged or rewritten through the API by
  anyone, tenant or operator. It is deliberately not `audit_logs`, which stays
  operator-only and carries full record snapshots; this carries no values at
  all.

  It lists newest-first without being asked — a feed in insertion order is a
  feed nobody can read — and it is excluded from `pull`/`apply`, because a
  declarative workspace has nothing to say about a log of what already happened.
  `--filter 'resource_id="<id>"'` is the one to know: it answers "who touched
  this device".

- **`stone invite accept <token>`** — redeem an invitation, wrapping the
  platform's `POST /api/org/invites/accept`. Every other side of the invitation
  flow was already here; the redeeming end was reachable only through the
  console, which left the CLI unable to finish the one flow it could start.

  The token is the `?token=` value from the invitation link, not the invite
  record's id, and the invitation is matched to the caller by email address.
  Redeeming sets `current_organization` only when it was blank, so the command
  points at `stone org switch` afterwards — which is also what writes the
  nats-cli context the new membership has no creds for yet.

- **`--limit` on every `ls`.** A single page rather than a truncated fetch, so
  it reaches the query: without it `ls` pages through every matching record,
  which is the wrong thing to ask of a server holding 40,000 things when you
  wanted the newest ten.

- **A drift test for collections, not just fields.** The three existing schema
  tests all start from `entitySpecs`, so a collection the CLI models *nothing*
  for is a collection no test looks at — the suite stays green and the feature
  is simply missing. That is exactly how `activity` went unnoticed through a
  schema refresh. A new platform collection now fails until someone writes a
  spec for it or records in `notAnEntity` why it has none.

### Changed

- **A 401 now says what to do about it.** PocketBase phrases it as "The request
  requires valid record authorization token to be set", which is accurate and
  tells nobody anything. This is the belt to the expired-token check's braces:
  it covers the endpoints that *do* answer 401 rather than serving a guest. A
  403 deliberately gets no hint — that one means the token is fine and the role
  is not, and logging in again would not change it.

- **An unknown-collection 404 names the likely cause.** PocketBase answers
  "Missing collection context." when a collection does not exist, which reads
  like a client bug and is nearly always the opposite — a CLI that knows about a
  collection the deployment has not got yet. `stone activity` against a
  pre-v0.8.0 server is exactly that case. A plain missing *record* is left
  alone; blaming the server version there would send someone checking a release
  they do not need to check.

- **The vendored schema is refreshed to platform v0.8.0** (was v0.6.0).

- An entity with no write verb is described as `Read <plural> records` rather
  than `Manage` in `stone --help`.

## [0.3.0] - 2026-09-17

### Removed

- **The `leaf-node` entity.** The platform dropped its `leaf_nodes` collection:
  an edge site is now an ordinary Thing whose agent has its leaf capabilities
  turned on, and the JetStream domain is computed from the Thing's `code` rather
  than stored. Gone here with it: `stone leaf-node` in every alias form, and
  `--synced-collections`, which was that entity's only multiselect field.

  This one did **not** fail quietly the way the `message-schema` removal below
  did — the collection is gone, so every `leaf-node` subcommand 404s. It also
  broke `stone pull`, which walks `entitySpecs` and returns on the first
  collection error: the pull wrote every other collection and then exited
  non-zero, which is a GitOps workflow that looks like it half-worked because it
  did.

  Existing `nats_users` and `nebula_hosts` rows that belonged to a leaf node
  are deliberately left working by the platform's migration — they are live
  credentials at real sites. Deactivate them from the console once each site runs
  against its Thing. Deactivate, not delete: revoking a Nebula certificate needs
  the record in the database to fingerprint it.

- **The `message-schema` entity, and the fields that pointed at it.** The
  platform dropped its `message_schemas` collection along with
  `thing_types.capabilities`, `thing_types.nats_role` and
  `thing_type_operations.schema`; nothing validated a payload against a stored
  schema at either end, and `nats_role` was a bridge to runtime NATS
  permissions that was never wired up. Gone here too: `stone message-schema`,
  `--capabilities` and `--nats-role` on `thing-type`, `--schema` on
  `thing-type-operation`, and `message_schemas` as a `--synced-collections`
  choice. **These flags did not fail before — PocketBase discards a write to a
  field a collection does not have, so they reported success and did nothing.**
  Per-operation `--capability` is unaffected and remains the place a capability
  is declared.

### Added

- **`stone thing provision`** — create a Thing and mint its NATS identity and
  Nebula host in **one server-side transaction**, wrapping the platform's
  `POST /api/org/things`.

  It sits beside `thing create` rather than replacing it. `create` writes one
  inventory record, which is what `apply` needs; `provision` stands a device up
  on the network. Doing that as three client calls is what the route exists to
  replace — a failure on the third left a signed NATS credential and an allocated
  overlay IP with nothing referencing either. The atomicity is real rather than
  decorative: pb-nats signs and publishes on `AfterCreateSuccess`, which
  PocketBase defers to transaction completion, so a rollback means NATS was never
  told anything happened.

  `--nats-mode` and `--nebula-mode` each take `none` (default), `auto` or
  `link`. `auto` NATS uses the organization's active account and its default
  role unless `--nats-role` names another; `auto` Nebula requires
  `--nebula-network` and `--nebula-ip`, because the route does not allocate an
  overlay address. `link` attaches an existing identity by id.

  The Thing's email (`<code>@<org-code>.thing.local`) and password are generated
  server-side, and the password is printed **once** — PocketBase stores only its
  hash. Attaching either identity requires owner or admin; a member may provision
  with both modes left at `none`.

- **`stone nebula`**, for the two overlay operations that are not record writes.

  `stone nebula ca-rotate prepare|commit|finish` rolls the organization's Nebula
  CA. It is a route (`POST /api/org/nebula-ca/rotate`) because
  `nebula_ca.updateRule` is operator-only — permitting the trigger through an
  update rule would mean a deny-list over the certificate, the private key and
  the rest of the CA material, on the record holding the trust anchor for the
  whole mesh.

  Three steps, and the wait between them is the feature. Nebula verification is
  mutual — each peer checks the other against its *own* local CA pool, with no
  chain and no fallback — and hosts pull their config on their own schedule, so
  one write carrying both the new trust bundle and the new certificate splits
  the mesh until propagation finishes. `prepare` publishes trust and moves no
  issuance, so it is reversible; `commit` switches issuance and re-signs every
  active host; `finish` drops the outgoing CA and is refused while any active
  host still holds one, naming the host.

  `stone nebula cert-audit` lists active hosts whose certificate no longer
  matches their network. pb-nebula signed host certificates at `/32` until
  v0.3.0, and Nebula builds a host's overlay route from the network in its
  certificate — so such a host reaches no peer while looking entirely healthy:
  active, in date, certificate present, config rendered, nothing logged. An
  edited `overlay_ip` lands a host here too. It is a route
  (`GET /api/org/nebula/cert-audit`) because answering it means parsing a Nebula
  certificate, which no client can do.

  Fix one host at a time with `stone nebula-host update <hostname> --renew`,
  redeploying each config as you go. There is deliberately no bulk verb:
  re-signing moves a certificate's fingerprint, and a fingerprint is what the
  revocation blocklist matches, so a sweep rewrites every peer config in the
  mesh.

- **Seven new `nebula-host` flags**, from pb-nebula v0.3.0: `--is-relay`,
  `--unsafe-networks`, `--unsafe-routes`, `--preferred-ranges`, `--mtu`,
  `--tun-device` and `--renew`. `is_relay` joins the `ls` columns, which now
  badge lighthouse and relay separately — a host can be both.

  `--unsafe-networks` and `--unsafe-routes` are two halves of the same feature
  living on *different* hosts: the first is signed into the gateway's
  certificate and authorizes it to route that subnet, the second goes on each
  host that wants to reach it. Neither derives the other.

  **These require a platform on pb-nebula v0.3.0 or newer** (the platform pins
  v0.3.2). Against v0.2.0 they name fields the collection does not have, so
  PocketBase discards the write and the command reports success — the same
  failure the `message-schema` removal above describes.

- **`routeOnlyFields` in the schema drift guard.** The existing tests catch a
  flag for a field that does not exist. They cannot catch a flag for a field
  that *does* exist and that a tenant is not allowed to write — the write simply
  404s at the rule layer, on a command that looks like every other update. That
  is the shape `nebula_ca.rotate` would take if anyone added a `--rotate` flag,
  and the `nats_accounts` signing-key triggers have been in that position all
  along with only a prose note guarding them.

- **`--code` on `organization`,** and `code` in its `ls` columns. The one
  globally unique identifier in the ecosystem: derived from the name when
  omitted, immutable once set, and baked into signed NATS account JWTs and
  printed labels — so an operator needs to be able to read it back. This had
  been missing since the platform added the field; the drift guard below is
  what found it.

### Changed

- **The vendored platform schema is refreshed again** (`cmd/testdata/schema.json`,
  now platform `754e421`). The drift guard caught `leaf_nodes` and nothing else
  across roughly twenty-five platform commits, which is the outcome the fixture
  exists to produce: one loud failure naming the collection, and silence about
  every field that did not move.

- **`thing create --active=false` is documented as the no-op it now is.** The
  platform's `hooks/active_flag.go` forces `active = true` on every Thing
  create, because `things.authRule` is `active = true` and a PocketBase bool has
  no schema default — an omitted field would land as false and the device could
  never authenticate. Deactivation is an update, and only an update. No flag
  changed; the help text and `CLAUDE.md` now say so.

- **Every write to `organization` is operator-only.** `organizations.deleteRule`
  lost its `owner = @request.auth.id` branch, joining `updateRule` and
  `createRule`, so delete was the last verb an org owner could reach. The verbs
  are left full rather than trimmed, because operators use this CLI too — for a
  tenant, all three now 404 at the rule layer. The spec comment says which and
  why.

### Fixed

- **Every error the CLI printed ended in `(0)`.** PocketBase sends the status as
  `status`; `PBError` only read `code`, which nothing populates. So the number
  in every error message was zero, and the distinction that matters most —
  a 400 from a validator versus a 404 from an update rule — was invisible. Both
  names are read now, with the HTTP status as a fallback.

- **Refreshed the vendored platform schema** (`cmd/testdata/schema.json`), which
  is what the drift tests check the field table against. It was three platform
  changes behind. Worth recording that the guard did its job unprompted: told
  only to refresh the fixture, it named all four removed fields, the collection
  that no longer exists, and the one field the CLI had never caught up to.

- **A stale comment on `--managed`** said the helpdesk export is remapped to
  carry the org **id**. It has carried the org **code** since the platform's
  ADR 0002.

## [0.2.0] - 2026-08-25

NATS was broken on Windows and macOS, in two ways that hid each other. If you
run either, this release is the reason to upgrade.

### Fixed

- **`sync-context` wrote nats-cli contexts where nats-cli does not read them.**
  stone resolved the directory with `xdg.ConfigHome`, which follows platform
  convention — `%LOCALAPPDATA%` on Windows, `~/Library/Application Support` on
  macOS. The nats tooling uses its own rule instead: `$XDG_CONFIG_HOME`, else
  `~/.config`, on every OS. So `sync-context` printed a path and reported
  success while writing to a dead drop, and the next command failed with
  `unknown context`. On Linux the two rules coincide, which is why this went
  unnoticed. Contexts now go where `nats` reads them; a stale file left at the
  old location is removed and named in the output, but only after stone
  confirms it wrote the file itself.

- **NATS commands silently connected to the wrong server.** With no
  `nats_context` set, stone passed an empty name to `natscontext.Connect`,
  whose documented behaviour is to fall through to whatever
  `nats context select` points at — or to `localhost:4222` when nothing is
  selected. `stone nats sub` would connect to an unrelated server and report
  `listening`, indistinguishable from a subject with no traffic. An unset or
  unresolvable context is now an error that names the path it looked for.

- **Async server errors were swallowed.** No `ErrorHandler` was registered, so
  `Permissions Violation for Subscription to ...` — the other reason a `sub`
  sits silent — never reached the user. Every connection now reports async
  errors, disconnects, and reconnects on stderr, and `sub` flushes the
  subscription and checks it survived before claiming to listen.

### Added

- **`--nats-context <name>`**, a persistent flag, to connect with a specific
  nats-cli context for one command. This is the deliberate override that
  replaces the accidental fallback above.

- **`stone context show` reports the NATS context file**: its full path, or
  `MISSING` plus the command that regenerates it.

- **Tests.** The repo had none, so CI's `go test ./...` ran over nothing. The
  entity table is now checked for internal consistency — dispatchable field
  types, known verbs, a `LookupKey` that names a real field, no flag colliding
  with one a command registers itself, no command name or alias claimed twice —
  and separately against a **vendored copy of the platform's schema**
  (`cmd/testdata/schema.json`), in both directions: a flag for a field the
  platform does not have, and a platform field the CLI neither exposes nor
  explains. Refresh the copy with
  `cp ../platform/schema.json cmd/testdata/schema.json`.

  This closes the drift the docs have always warned about: the field list is
  hand-maintained, and until now nothing would tell you it had fallen behind.

  The NATS context path has a guard of its own: a test writes a context through
  the real sync path and asserts `natscontext` can then find it by name. That
  is the contract the bug above broke, and it is the kind that only fails on an
  OS the author is not using.

## [0.1.0] - 2026-08-22

First tagged release, and the first one you do not have to compile yourself.
The CLI has been in use against the platform for months; this tag marks the
point at which it is packaged for other people to run.

### Added

Summarising the state at first tag rather than the path to it:

- **Contexts.** Named bundles of server URL, auth token, current organization,
  optional NATS context and optional workspace path, under
  `$XDG_CONFIG_HOME/stone/`, with `0600` on anything secret-bearing. One binary
  points at `local`, `staging` and `prod` without re-typing connection details.
- **Auth and organizations.** `auth login` / `whoami` / `logout`, and
  `org switch`, which updates `users.current_organization` **on the server** so
  the console and the CLI agree on context.
- **Typed CRUD over the platform's collections.** Things, locations, both type
  collections, the thing-type contract graph, message schemas, memberships,
  invites, the `nats_*` and `nebula_*` collections, and leaf nodes — generated
  from one declarative table, so every entity behaves the same way and the name
  aliases are forgiving. Lookup by 15-char id or by natural key (`code`, `name`,
  `hostname`).
- **Declarative workspaces.** `pull` writes one YAML file per record;
  `apply` reconciles them back through PocketBase's transactional `/api/batch`.
  Idempotent, and it never deletes — a one-way additive upsert, not a
  convergence loop.
- **NATS, JetStream and KV.** `nats pub/sub/req`, `kv` get/put/del/ls/watch plus
  bucket lifecycle, and `js stream` management, reusing your existing `nats` CLI
  contexts so JetStream domains are honoured.
- **Per-organization NATS credentials.** Credentials are per-membership on this
  platform, so `org switch` re-issues the local `.creds` and a matching
  `nats` CLI context. `nats creds rotate` rotates your own through the
  platform's dedicated route, which every role may call.
- **Scripting discipline.** `-o table|json|yaml`, structured output on stdout and
  human messages plus generated passwords on stderr, so
  `stone thing create … --random-password -o json | jq .id` does the right thing.
- **`SKILLS.md` and a Claude Code skill**, describing the same command surface
  for humans and for an assistant driving the CLI.

### Notes

- **The field list is hand-maintained**, not derived from the platform's
  `schema.json`, so a given release can lag a platform release. If a field
  exists in the console but has no flag here, that is why — see `cmd/entity.go`.
- `auth login` is interactive by design; credentials cannot be discovered by the
  CLI, which is also what stops an assistant authenticating as you.

[Unreleased]: https://github.com/stone-age-io/stone-cli/compare/v0.5.1...HEAD
[0.5.1]: https://github.com/stone-age-io/stone-cli/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/stone-age-io/stone-cli/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/stone-age-io/stone-cli/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/stone-age-io/stone-cli/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/stone-age-io/stone-cli/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/stone-age-io/stone-cli/releases/tag/v0.1.0
