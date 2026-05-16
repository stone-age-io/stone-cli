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
   - If empty and the user's task touches an **org-scoped** collection (everything except `organization`, `membership`, `invite`, and `nats-account`), pick one:
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

Always pass `--output json` (or `-o json`) when you intend to parse the result. Default table output is for humans and is not stable. Example:

```sh
stone thing ls -o json | jq '.[] | select(.code == "sensor-42") | .id'
```

The persistent flags `--context <name>` (override active context) and `--debug` (log HTTP to stderr) work on every command — use `--debug` when troubleshooting.

## Entity model — typed CRUD

Verbs `ls / create / update / delete / edit` are synthesized from a single `EntitySpec` table in `cmd/entity.go`. Naming is flexible: `stone thing`, `stone things`, `stone thing_type`, `stone thing-types` all resolve.

| Entity | Org-scoped | Verbs |
|---|---|---|
| `thing`, `location`, `thing-type`, `thing-type-operation`, `message-schema` | yes | full |
| `membership`, `invite` | no | full |
| `organization` | no | full |
| `nats-user`, `nats-role`, `nats-import`, `nats-export` | yes | full |
| `nebula-network`, `nebula-host` | yes | full |
| `nats-account`, `nebula-ca` | partial | `ls / update / edit` only |

### Field flag rules

- **Relations** (`--type`, `--location`, `--owner`, etc.) take a **15-char PocketBase id only**. There is no name-to-id resolution. Discover ids via `stone <type> ls -o json` first.
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

stone thing edit <thing_id>        # opens $EDITOR with YAML, PATCHes on save
```

## Pull / apply (GitOps workflow)

Use when the user wants to manage many records declaratively, version them in git, or apply changes from a YAML directory.

```sh
stone pull                    # writes <workspace>/<collection>/<code-or-id>.yaml
stone apply                   # walks workspace, batches up to 50/request via /api/batch
stone apply path/to/file.yaml # apply specific files only
```

Important semantics:
- Records with an `id` field are PATCHed. Records without are POSTed, and the server-assigned id is **written back into the file**.
- `apply` is idempotent — safe to re-run.
- `apply` **does not delete** records that exist on the server but are absent locally. Use `stone <type> delete <id>` for that.
- Org-scoped records get `organization` auto-injected on create if missing — relies on the current org being set.
- Server-managed fields (`collectionId`, `collectionName`, `created`, `updated`) are stripped by `pull` and ignored by `apply`.

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
- **HTTP 400 from PocketBase on a relation field** — likely passed something that isn't a 15-char id. Re-look it up with `ls -o json`.

## Things not to do

- Don't invent record ids. Always look up relations first.
- Don't write a per-entity command — there isn't one. Everything routes through `entity.go`'s `EntitySpec` table.
- Don't run `stone apply` against a workspace you didn't `pull` from or hand-author with knowledge of the schema — apply will dutifully create records.
- Don't expect `apply` to delete things. It only creates and updates.
- Don't bypass `stone org switch` by editing `context.yaml` directly — you'll skip the per-org NATS creds sync.
