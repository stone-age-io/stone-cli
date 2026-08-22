# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) — with the pre-1.0
caveat that a minor version may break something. Pin what you deploy.

History before `0.1.0` is not reconstructed here; `git log` is the record for
that period, and this file starts where the versioned releases do.

## [Unreleased]

_Nothing yet._

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

[Unreleased]: https://github.com/stone-age-io/stone-cli/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/stone-age-io/stone-cli/releases/tag/v0.1.0
