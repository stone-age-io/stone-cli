# SKILLS.md

This document describes the `stone` CLI's capability surface for integration with AI assistants and automation. The companion file at `.claude/skills/stone/SKILL.md` is the Claude Code harness version (imperative, auto-loaded when the description matches the user's request); this file is the human-readable reference.

## Scope

`stone` is the CLI for the Stone Age IoT Platform. An assistant that can shell out to `stone` can:

- Manage tenant resources (things, locations, thing-types, memberships, NATS users/roles, Nebula networks/hosts) with typed CRUD.
- Operate declaratively on a YAML workspace via `pull` and `apply` (PocketBase `/api/batch`, up to 50 ops per request, idempotent, no deletes).
- Publish, subscribe, request on NATS, including JetStream publish.
- Read and write JetStream KV buckets.
- Administer JetStream streams (`stone js stream`) and KV bucket lifecycle (`stone kv bucket`). Consumer management is intentionally out of scope — use the `nats` CLI.

The CLI authenticates to a PocketBase server and reuses the user's nats-cli contexts (with per-org sync after `org switch`).

## Required bootstrap

Per-user state lives under `$XDG_CONFIG_HOME/stone/`. Before doing real work, four preconditions must hold; an assistant should check each in order and only fix what is missing:

| Step | Check | Fix |
|---|---|---|
| 1. Context | `stone context ls` | `stone context create <name> --url <server> [--nats-url nats://...]` |
| 2. Auth | `stone auth whoami` | `stone auth login` (interactive; user must supply email/password) |
| 3. Organization | `stone org current` | `stone org ls` then `stone org switch <name>` |
| 4. Workspace (optional, for pull/apply) | check `context.yaml`'s `workspace:` | `stone pull --set-workspace .` |

Step 2 cannot be automated by an assistant — `auth login` prompts for credentials. Surface it to the user.

Step 3 is required for any collection the CLI auto-filters by org (the `OrgScoped` flag in `cmd/entity.go`). That covers everything except `organization` and `membership`. `membership` records *do* carry an `organization` relation, but the CLI deliberately doesn't filter them by current org — users typically want to see their memberships across every org they belong to. `organization` access is gated server-side by `is_operator`.

If the user needs NATS, ensure `--nats-url` was passed at step 1 (or re-run `org switch --nats-url ...`). Without it, `org switch` prints `nats-sync: skipped — no NATS URL on this stone context` and downstream `nats`/`kv` commands fall back to the user's default nats-cli context.

## Output discipline

All commands accept `--output json|yaml|table` (`-o` short form). Use `-o json` when programmatically consuming output. Table output is for humans and is not stable across versions.

Persistent flags on every command:

- `--context <name>` — override the active context for a single invocation.
- `--output <fmt>` / `-o` — pick format.
- `--debug` — log HTTP requests/responses to stderr (request bodies truncated at 4 KB).

## Entity surface

Verbs `ls / get / create / update / delete / edit` are derived from a single declarative table (`EntitySpec` in `cmd/entity.go`). Name aliases mean `stone thing`, `stone things`, `stone thing_type`, and `stone thing-types` all resolve to the right command.

| Entity | Collection | Org-scoped | Lookup key | Verbs |
|---|---|---|---|---|
| `thing` | `things` | yes | `code` | full |
| `location` | `locations` | yes | `code` | full |
| `location-type` | `location_types` | yes | `code` | full |
| `thing-type` | `thing_types` | yes | `code` | full |
| `thing-type-operation` | `thing_type_operations` | yes | `name` | full |
| `organization` | `organizations` | no | `name` | full |
| `membership` | `memberships` | no | — (id only) | full |
| `invite` | `invites` | yes | `email` | full |
| `nats-user` | `nats_users` | yes | `nats_username` | full |
| `nats-role` | `nats_roles` | yes | `name` | full |
| `nats-import` | `nats_account_imports` | yes | `name` | full |
| `nats-export` | `nats_account_exports` | yes | `name` | full |
| `nebula-network` | `nebula_networks` | yes | `name` | full |
| `nebula-host` | `nebula_hosts` | yes | `hostname` | full |
| `leaf-node` | `leaf_nodes` | yes | `code` | full |
| `nats-account` | `nats_accounts` | yes | `name` | `ls / get / update / edit` |
| `nebula-ca` | `nebula_ca` | yes | `name` | `ls / get / update / edit` |

`get`, `update`, `delete`, and `edit` take a positional `<id|lookup-key>`: either a 15-char PocketBase id or the entity's lookup key from the table above. Key lookups are exact-match and scoped to the current organization; zero or multiple matches fail, with candidate ids listed on ambiguity.

`get` (alias `show`) prints one record. Both `get` and `ls` accept `--fields a,b,c` for server-side projection (PocketBase's `fields` query param); on `ls` table output the requested fields become the columns.

`edit` opens the record as YAML in `$EDITOR` and PATCHes on save.

### Field types

| Type | Flag form | Notes |
|---|---|---|
| string, int, bool | `--name foo`, `--validity-years 5`, `--active` / `--active=false` | |
| select | `--capability publish` | validated against a whitelist |
| multiselect | `--synced-collections things,locations` | comma-separated |
| relation (id) | `--type abc123def456ghi` | **15-char PocketBase id only** — natural keys resolve on positional args, never on relation flags |
| relation list (ids) | `--operations id1,id2` or repeated flag | |
| JSON | `--metadata '{"k":"v"}'`, `--metadata @file.json`, `--metadata -` | inline, file, or stdin |

Relation flags deliberately have no name-to-id resolver: discover ids via `stone <type> get <key> --fields id -o json` or `stone <type> ls -o json` first.

### Auth-collection ergonomics

`thing`, `nats-user`, `nebula-host`, and `leaf-node` are PocketBase auth collections. The CLI smooths over two PB requirements so callers don't have to think about them:

- When a non-empty `password` is sent on create or update, `passwordConfirm` is mirrored to match, and `emailVisibility` defaults to `true` if unset. This applies to typed CRUD, `apply`, and `edit`.
- On `create`, pass `--random-password` instead of `--password` to have the CLI generate a 32-char URL-safe password (`crypto/rand`, base64). The generated value is printed once to **stderr** so stdout stays clean for parsers. `--password` and `--random-password` are mutually exclusive; exactly one is required.

```sh
stone thing create --email reader-01@things.example.com --code reader-01 \
    --type <thing_type_id> --random-password -o json
# stderr: generated password: <value>
# stdout: { ...record... }
```

## Declarative workflow

```sh
stone pull                                  # writes <workspace>/<collection>/<lookup-key>.yaml per record
stone apply                                 # POSTs through /api/batch in 50-record transactions
stone apply path/to/dir path/to/file.yaml   # restrict to specific paths
```

Properties:

- **Idempotent.** Records with an `id` are PATCHed; records without are POSTed and the returned id is written back into the file.
- **Friendly filenames.** Files are named by the entity's lookup key, falling back to `name`, then id. Filename collisions get a `-<id>` suffix, and pulls are sorted by id so the suffix lands on the same record across runs. Filenames are cosmetic — `apply` keys on the `id` field inside each file.
- **No deletes.** Records present on the server but absent locally are left alone. For deletion, use `stone <type> delete <id|key>` or the web UI.
- **Org-scoped auto-fill.** On create, the current organization is injected into org-scoped records that don't already have one.
- **Server-managed fields ignored.** `collectionId`, `collectionName`, `created`, `updated` are stripped on pull and ignored on apply.

Put the workspace under git for diff, history, and review.

## NATS and JetStream

```sh
stone nats pub <subject> <payload>                    # payload may be literal, @file, or -
stone nats pub <subject> @msg.json --js               # JetStream publish, prints ack
stone nats sub <subject>                              # subscribe (Ctrl-C to stop)
stone nats req <subject> <payload> --timeout 5s

stone kv get <bucket> <key>
stone kv put <bucket> <key> <value>                   # value: literal, @file, or -
stone kv del <bucket> <key>
stone kv watch <bucket>
stone kv ls <bucket>                                  # list keys in <bucket>

stone kv bucket ls                                    # list all KV buckets
stone kv bucket info <name>
stone kv bucket create <name> --history 5 --ttl 720h
stone kv bucket delete <name>

stone js stream ls
stone js stream info <name>
stone js stream create <name> --subject 'foo.>' --max-age 24h --storage file
stone js stream create <name> --config stream.yaml    # advanced
stone js stream purge <name>
stone js stream delete <name>

stone nats sync-context                               # re-issue per-org creds after rotation
```

`stone nats sync-context` is the rotation hook: re-run after `stone nats-user update <id> --regenerate` or any time the linked `nats_users` record's `creds_file` changes.

## Per-org NATS creds

When `nats_url` is set on the stone context, `stone org switch <org>`:

1. Looks up the calling user's `memberships` record for `<org>`.
2. Reads the linked `nats_users` record and its `creds_file`.
3. Writes `~/.config/stone/creds/stone-<ctx>-<org>.creds` and `~/.config/nats/context/stone-<ctx>-<org>.json`.
4. Updates the stone context's `nats_context` to that name.

Pass `--set-nats-default` to also point the user's `nats` CLI default at it. Pass `--no-nats` to skip the sync. Pass `--verbose` to either `org switch` or `nats sync-context` to print user/membership/NATS user ids on stderr.

The `nats-sync: skipped` line is informational, not an error:

| Reason | Meaning |
|---|---|
| `no NATS URL on this stone context` | `nats_url` was never set; re-create the context or pass `--nats-url` to a future `org switch` |
| `no membership found for this user+org` | acting as an operator on an org you aren't a member of; creds are per-membership |
| `membership has no linked nats_user` | platform hasn't provisioned a NATS user for this membership yet |
| `(--no-nats)` | the flag was passed |

## Credential lifecycle — four distinct operations

Do not substitute one of these for another. They differ in blast radius.

| Intent | Command | Effect |
|---|---|---|
| Replace my own credential | `stone nats creds rotate` | New creds for the caller's own identity. Any role, incl. badge. No id — derived from the token. |
| Replace someone else's | `stone nats-user update <username> --regenerate` | New creds for that identity. Owner/admin. |
| Kill a credential | `stone nats-user update <username> --revoke` | NATS rejects it **immediately and permanently**. Owner/admin. |
| Decommission the device | `stone thing update <code> --active=false` | Signs the device out, blocks re-login, **and** revokes its NATS credential. Owner/admin. Also on `leaf-node`. |

Rules a caller must not get wrong:

- **Rotation is not revocation.** `--regenerate` leaves the previous credential valid until it expires. After a suspected compromise use `--revoke`.
- **Never suggest `--active` on `nats-user`; the flag does not exist, deliberately.** `pb-nats` reads that field and consults it nowhere in JWT generation, so clearing it would recolour a status badge while the client kept publishing. It is readable as a status column only. The disconnect operation is `--revoke`.
- **`--active=false` on a thing or leaf-node is destructive to device operation, not a label.** Reactivating issues a *fresh* NATS credential; the previous `.creds` file stays revoked forever and must be replaced on the device. Confirm intent before running it.
- `active` round-trips through `pull`/`apply`. A workspace file carrying `active: false` decommissions real hardware on the next apply.
- Re-run `stone nats sync-context` after any operation that changes the caller's own credential.

Org account signing keys (owner/admin, active org, no record id):

```sh
stone nats account-keys add-signing              # routine; existing user JWTs stay valid
stone nats account-keys remove-signing <pubkey>  # last remaining key cannot be removed
stone nats account-keys rotate                   # EMERGENCY: invalidates every user JWT in the account
```

## Nebula overlay — two operations that are not record writes

The records are ordinary entities (`nebula-ca`, `nebula-network`, `nebula-host`).
These two live under `stone nebula` because a PocketBase rule cannot express them.

```sh
stone nebula cert-audit                 # active hosts whose certificate no longer matches their network
stone nebula ca-rotate prepare|commit|finish
```

Rules a caller must not get wrong:

- **`ca-rotate` is three steps with a wait between them, and the wait is the feature.** Nebula verification is mutual — each peer checks the other against its *own* local CA pool, with no chain and no fallback — and hosts pull their config on their own schedule. `prepare` publishes trust in the incoming CA and moves no issuance, so it is fully reversible. Only once **every** host has fetched its config does `commit` become safe; it switches issuance and re-signs every active host, with both CAs trusted throughout. `finish` drops the outgoing CA. Never run the three back to back — that is the single write the three-step design exists to avoid, and it splits the mesh for as long as propagation takes.
- **`finish` is interlocked and the refusal is informative.** It is refused while any active host still holds a certificate from the outgoing CA, and the error names the host. Deploy that host's config and retry; do not look for a force flag, there isn't one.
- **A CA cannot be renewed, only rotated.** Check `stone nebula-ca ls` for the expiry and start months ahead, not weeks — the wait in the middle cannot be compressed.
- **`cert-audit` reports a failure that is invisible everywhere else.** pb-nebula signed host certificates at `/32` until v0.3.0; Nebula builds the host's overlay route from the certificate's network, so such a host reaches no peer while looking entirely healthy — active, in date, certificate present, config rendered, no errors. Editing a host's `overlay_ip` after issue has the same effect.
- **Fix stale hosts one at a time**, with `stone nebula-host update <hostname> --renew`, redeploying each config as you go. Do not script a sweep: re-signing moves a certificate's fingerprint, and a fingerprint is what the revocation blocklist matches, so a bulk re-issue rewrites every peer config in the mesh.
- **`--active=false` on a `nebula-host` is revocation**, and it now reaches every network under the same CA rather than just the host's own — Nebula's trust boundary is the CA. It takes effect when each *peer's* config is redeployed, not instantly. Deactivate to revoke; deleting the record leaves the certificate trusted until it expires, because a fingerprint that is not in the database cannot be blocklisted.
- **`--is-relay` needs `--public-host-port`.** Without it the host listens on an ephemeral port while every peer is handed its overlay IP as a usable path.
- **`--unsafe-networks` and `--unsafe-routes` are two halves on two different hosts.** The first is signed into the *gateway's* certificate and authorizes it to route that subnet; the second goes on every host that wants to reach it, with `via` set to the gateway's overlay IP. Neither derives the other, and setting only one moves no traffic.

Requires a platform on **pb-nebula v0.3.0+**. Against v0.2.0 the routes 404 and the newer host flags name fields the collection lacks, so the write is discarded and the command still reports success.

## Known limitations

- Relation flags do not resolve names — pass 15-char PocketBase ids only. (Positional record args on `get`/`update`/`delete`/`edit` *do* accept natural keys.)
- `apply` does not delete server records absent from the workspace.
- No JetStream **consumer** management (use `nats` CLI).
- `nats-account` and `nebula-ca` are **operator-only for every field** — both `updateRule`s admit no tenant role, so an owner/admin PATCH of any field on either returns 404. The tenant operations live behind routes: `stone nats account-keys` and `stone nebula ca-rotate`.
- File fields have no CLI upload path: `locations.floorplan`, `organizations.logo`. Use the console.

## Configuration files

```
$XDG_CONFIG_HOME/stone/
├── config.yaml                       # active_context, output default
├── contexts/
│   └── <name>/context.yaml           # url, auth (token, email, user_id), current_organization, nats_url, nats_context, workspace
└── creds/
    └── stone-<ctx>-<org>.creds       # per-org NATS creds (written by org switch)

$XDG_CONFIG_HOME/nats/context/
└── stone-<ctx>-<org>.json            # matching nats-cli context (written by org switch)
```

Permissions on the context and creds files are 0600.
