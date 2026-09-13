# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) — with the pre-1.0
caveat that a minor version may break something. Pin what you deploy.

History before `0.1.0` is not reconstructed here; `git log` is the record for
that period, and this file starts where the versioned releases do.

## [Unreleased]

### Removed

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

[Unreleased]: https://github.com/stone-age-io/stone-cli/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/stone-age-io/stone-cli/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/stone-age-io/stone-cli/releases/tag/v0.1.0
