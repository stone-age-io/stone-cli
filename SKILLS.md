# SKILLS.md

This document describes the `stone` CLI's capability surface for integration with AI assistants and automation. The companion file at `.claude/skills/stone/SKILL.md` is the Claude Code harness version (imperative, auto-loaded when the description matches the user's request); this file is the human-readable reference.

## Scope

`stone` is the CLI for the Stone Age IoT Platform. An assistant that can shell out to `stone` can:

- Manage tenant resources (things, locations, thing-types, message schemas, memberships, NATS users/roles, Nebula networks/hosts) with typed CRUD.
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
| `thing-type` | `thing_types` | yes | `code` | full |
| `thing-type-operation` | `thing_type_operations` | yes | `name` | full |
| `message-schema` | `message_schemas` | yes | `name` | full |
| `organization` | `organizations` | no | `name` | full |
| `membership` | `memberships` | no | — (id only) | full |
| `invite` | `invites` | yes | `email` | full |
| `nats-user` | `nats_users` | yes | `nats_username` | full |
| `nats-role` | `nats_roles` | yes | `name` | full |
| `nats-import` | `nats_account_imports` | yes | `name` | full |
| `nats-export` | `nats_account_exports` | yes | `name` | full |
| `nebula-network` | `nebula_networks` | yes | `name` | full |
| `nebula-host` | `nebula_hosts` | yes | `hostname` | full |
| `nats-account` | `nats_accounts` | yes | `name` | `ls / get / update / edit` |
| `nebula-ca` | `nebula_ca` | yes | `name` | `ls / get / update / edit` |

`get`, `update`, `delete`, and `edit` take a positional `<id|lookup-key>`: either a 15-char PocketBase id or the entity's lookup key from the table above. Key lookups are exact-match and scoped to the current organization; zero or multiple matches fail, with candidate ids listed on ambiguity.

`get` (alias `show`) prints one record. Both `get` and `ls` accept `--fields a,b,c` for server-side projection (PocketBase's `fields` query param); on `ls` table output the requested fields become the columns.

`edit` opens the record as YAML in `$EDITOR` and PATCHes on save.

### Field types

| Type | Flag form | Notes |
|---|---|---|
| string, int, bool | `--name foo`, `--validity-years 5`, `--active true` | |
| select | `--capability publish` | validated against a whitelist |
| multiselect | `--capabilities publish,subscribe` | comma-separated |
| relation (id) | `--type abc123def456ghi` | **15-char PocketBase id only** — natural keys resolve on positional args, never on relation flags |
| relation list (ids) | `--operations id1,id2` or repeated flag | |
| JSON | `--metadata '{"k":"v"}'`, `--metadata @file.json`, `--metadata -` | inline, file, or stdin |

Relation flags deliberately have no name-to-id resolver: discover ids via `stone <type> get <key> --fields id -o json` or `stone <type> ls -o json` first.

### Auth-collection ergonomics

`thing`, `nats-user`, and `nebula-host` are PocketBase auth collections. The CLI smooths over two PB requirements so callers don't have to think about them:

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
- **Friendly filenames.** Files are named by the entity's lookup key (message-schemas: `namespace__name__version`; fallback `name`, then id). Filename collisions get a `-<id>` suffix, and pulls are sorted by id so the suffix lands on the same record across runs. Filenames are cosmetic — `apply` keys on the `id` field inside each file.
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

`stone nats sync-context` is the rotation hook: re-run after `stone nats-user update <id> --regenerate true` or any time the linked `nats_users` record's `creds_file` changes.

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

## Known limitations

- Relation flags do not resolve names — pass 15-char PocketBase ids only. (Positional record args on `get`/`update`/`delete`/`edit` *do* accept natural keys.)
- `apply` does not delete server records absent from the workspace.
- No JetStream **consumer** management (use `nats` CLI).
- `nats-account` and `nebula-ca` updates that touch limits or infrastructure fields require operator-level credentials server-side. Org admins can only trigger rotation via `rotate_keys: true`.

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
