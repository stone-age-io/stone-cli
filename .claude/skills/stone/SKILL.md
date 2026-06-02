---
name: stone
description: Use when the user wants to interact with the Stone Age IoT Platform via the `stone` CLI — managing tenant resources (things, locations, thing-types, message schemas, memberships, NATS users/roles, Nebula hosts), applying declarative YAML workspaces (pull/apply GitOps), publishing/subscribing on the platform's NATS, or reading/writing JetStream KV. Triggers on commands starting with `stone`, requests like "create a thing", "switch org", "pull the workspace", "publish to NATS on the platform".
---

# stone CLI skill

`stone` is the official CLI for the Stone Age IoT Platform. It talks to a PocketBase server for tenant data and to NATS/JetStream for messaging. This skill teaches you when to use it and how to chain its commands safely.

## Bootstrap — run before any real work

Before invoking entity, NATS, or pull/apply commands, verify the CLI's per-user state. Walk this chain top-to-bottom and stop at the first step that's already satisfied.

State lives under `$XDG_CONFIG_HOME/stone/` (e.g. `~/.config/stone/`). It is **per OS user**, not per repo. If a context already exists from an earlier session, reuse it — don't recreate.

1. **Context exists?**
   ```sh
   stone context ls
   ```
   - If empty, you need a server URL from the user. Ask, then:
     ```sh
     stone context create <name> --url <https://pb.example.com> [--nats-url nats://host:4222]
     ```
   - Always include `--nats-url` if the user mentioned NATS, JetStream, KV, or per-org creds. Without it, NATS sync silently skips on `org switch`.

2. **Authenticated?**
   ```sh
   stone auth whoami
   ```
   - On failure: `stone auth login` is interactive (prompts for email + password). You cannot supply these. Surface the requirement and let the user run it themselves, or have them pass `--email`/`--password` flags if they prefer.

3. **Current organization set?**
   ```sh
   stone org current
   ```
   - If empty and the user's task touches a collection the CLI auto-filters by org (`OrgScoped: true` in `cmd/entity.go` — everything except `organization` and `membership`), pick one. (`membership` carries an org relation but is intentionally not auto-filtered so users see memberships across every org; `organization` access is gated server-side by `is_operator`.)
     ```sh
     stone org ls
     stone org switch <name>          # also writes per-org NATS creds when nats_url is set
     ```
   - `org switch` always switches the org server-side. If it prints `nats-sync: skipped — <reason>`, that's expected when `nats_url` isn't set or the user has no NATS user provisioned; don't treat it as failure unless the user actually needs NATS.

4. **Workspace set?** (only for `pull`/`apply`)
   ```sh
   stone pull --set-workspace .       # from an empty dir, ideally under git
   ```

## Output discipline

Always pass `--output json` (or `-o json`) when you intend to parse the result. Default table output is for humans and is not stable. Examples:

```sh
stone thing get sensor-42 --fields id -o json      # single record by code; projected server-side
stone thing ls -o json | jq '.[].id'               # all ids
```

The persistent flags `--context <name>` (override active context) and `--debug` (log HTTP to stderr) work on every command — use `--debug` when troubleshooting.

## Entity model — typed CRUD

Verbs `ls / get / create / update / delete / edit` are synthesized from a single `EntitySpec` table in `cmd/entity.go`. Naming is flexible: `stone thing`, `stone things`, `stone thing_type`, `stone thing-types` all resolve.

| Entity | Org-scoped | Verbs |
|---|---|---|
| `thing`, `location`, `thing-type`, `thing-type-operation`, `message-schema` | yes | full |
| `invite` | yes | full |
| `nats-user`, `nats-role`, `nats-import`, `nats-export` | yes | full |
| `nebula-network`, `nebula-host` | yes | full |
| `nats-account`, `nebula-ca` | yes | `ls / get / update / edit` only |
| `membership` | no (org relation present, but not auto-filtered) | full |
| `organization` | no (gated server-side by `is_operator`) | full |

### Positional record args: id or natural key

`get`, `update`, `delete`, and `edit` accept either a 15-char PocketBase id or the entity's lookup key — exact match, scoped to the current org. Zero or multiple matches fail (ambiguity lists the candidate ids; fall back to the id then). Prefer keys for one-off operations; you usually don't need an `ls`+`jq` round trip first.

| Entities | Lookup key |
|---|---|
| `thing`, `location`, `thing-type` | `code` |
| `nebula-host` | `hostname` |
| `nats-user` | `nats_username` |
| `invite` | `email` |
| `membership` | — id only |
| everything else | `name` |

`get` (alias `show`) prints one record. `--fields a,b,c` projects server-side on both `get` and `ls`; on `ls` table output the requested fields become the columns.

```sh
stone thing get sensor-42 -o json
stone nebula-host get edge-west --fields hostname,overlay_ip
stone thing ls --fields code,name -o json
```

### Field flag rules

- **Relations** (`--type`, `--location`, `--owner`, etc.) take a **15-char PocketBase id only**. Natural keys resolve on positional args, never on relation flags. Discover ids via `stone <type> get <key> --fields id -o json` or `stone <type> ls -o json` first.
- **Multi-relations** (`--operations id1,id2` or repeated `--operations id1 --operations id2`) accept comma-separated lists.
- **JSON fields** (`--metadata`, `--schema`, `--firewall_inbound`, …) accept:
  - inline JSON: `--metadata '{"k":"v"}'`
  - file: `--metadata @./meta.json`
  - stdin: `--metadata -`
- **Selects/multiselects** (e.g. `--capabilities publish,subscribe`) are validated against a whitelist; the CLI prints the choices in `--help`.

### Auth collections (`thing`, `nats-user`, `nebula-host`)

These collections are PocketBase auth records. The CLI handles two PB quirks for you:

- `passwordConfirm` is mirrored from `password` automatically; never pass it yourself.
- `emailVisibility` defaults to `true` on create when a password is supplied.

On `create` you must pass exactly one of:
- `--password '<value>'` — explicit password (min 8 chars).
- `--random-password` — CLI generates a 32-char URL-safe password and prints it **to stderr** (stdout stays clean for parsers). Prefer this for non-interactive flows where you'll capture stderr.

### Examples

```sh
stone location create --name "HQ" --code hq -o json
stone thing-type create --name "Temp Sensor" --code temp \
    --subject-prefix telemetry.sensors --capabilities publish,subscribe -o json

# Explicit password
stone thing create --email s42@example.com --password 'changeMe123!' \
    --code sensor-42 --type <thing_type_id> --location <location_id> -o json

# CLI-generated password (printed once to stderr)
stone thing create --email s43@example.com --random-password \
    --code sensor-43 --type <thing_type_id> -o json 2> pw.txt

stone thing get sensor-42 --fields code,name,location   # lookup by code or id
stone thing edit sensor-42         # opens $EDITOR with YAML, PATCHes on save
```

## Pull / apply (GitOps workflow)

Use when the user wants to manage many records declaratively, version them in git, or apply changes from a YAML directory.

```sh
stone pull                    # writes <workspace>/<collection>/<lookup-key>.yaml
stone apply                   # walks workspace, batches up to 50/request via /api/batch
stone apply path/to/file.yaml # apply specific files only
```

Important semantics:
- Records with an `id` field are PATCHed. Records without are POSTed, and the server-assigned id is **written back into the file**.
- `apply` is idempotent — safe to re-run.
- `apply` **does not delete** records that exist on the server but are absent locally. Use `stone <type> delete <id|key>` for that.
- Org-scoped records get `organization` auto-injected on create if missing — relies on the current org being set.
- Server-managed fields (`collectionId`, `collectionName`, `created`, `updated`) are stripped by `pull` and ignored by `apply`.
- Filenames are the entity's lookup key (message-schemas: `namespace__name__version`; fallback `name`, then id), with a `-<id>` suffix on collisions. They are cosmetic — `apply` identifies records solely by the `id` field inside each file, so renaming files is safe.

## NATS / JetStream

`stone` reuses the user's nats-cli contexts. After `org switch` (with `nats_url` configured), a per-org context named `stone-<ctx>-<org>` is written and selected.

```sh
stone nats pub demo.hello 'world'
stone nats pub events.x @./payload.json --js   # JetStream publish; prints ack
stone nats sub 'demo.>'
stone nats req svc.echo 'ping' --timeout 5s

stone kv get twins device.42
stone kv put twins device.42 '{"online":true}'
stone kv put twins device.42 @./twin.json
stone kv ls twins                              # list keys in the bucket
stone kv watch twins

stone kv bucket ls                             # list all KV buckets
stone kv bucket info twins
stone kv bucket create twins --history 5 --ttl 720h
stone kv bucket delete twins

stone js stream ls
stone js stream create twins --subject 'twins.>' --max-age 24h --storage file

stone nats sync-context        # re-issue per-org creds after key rotation
```

`stone` does **not** manage JetStream consumers — use the `nats` CLI for that.

## Common failure modes and how to react

- **`not authenticated. run: stone auth login`** — auth token is missing or expired. Surface to user; they run `stone auth login`.
- **`no active context`** — bootstrap step 1 was skipped.
- **`collection is org-scoped but no current organization set`** — bootstrap step 3.
- **`nats-sync: skipped — no NATS URL on this stone context`** — `nats_url` was never set. If the user actually needs NATS, run `stone context create` again or `stone org switch <org> --nats-url nats://...` to set it.
- **`nats-sync: skipped — no membership found for this user+org`** — the authenticated user is an operator on an org they aren't a member of; NATS creds are per-membership. Not a bug.
- **HTTP 400 from PocketBase on a relation field** — likely passed something that isn't a 15-char id. Re-look it up with `get <key> --fields id -o json` or `ls -o json`.
- **`multiple <plural> match <key> "..."`** — the natural key is ambiguous in this org. Use one of the 15-char ids listed in the error.

## Things not to do

- Don't invent record ids. Always look up relations first.
- Don't write a per-entity command — there isn't one. Everything routes through `entity.go`'s `EntitySpec` table.
- Don't run `stone apply` against a workspace you didn't `pull` from or hand-author with knowledge of the schema — apply will dutifully create records.
- Don't expect `apply` to delete things. It only creates and updates.
- Don't bypass `stone org switch` by editing `context.yaml` directly — you'll skip the per-org NATS creds sync.
