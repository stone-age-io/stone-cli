# stone

Opinionated CLI for the [Stone Age IoT Platform](https://github.com/stone-age-io).

`stone` complements the web console with command-line ergonomics for the things
the platform actually does: managing tenant resources (things, locations, thing
types, message schemas), publishing/subscribing to NATS, reading JetStream KV,
and syncing a workspace of YAML files declaratively (GitOps style).

## Quickstart

```sh
# 1) Build
go build -o stone

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
    --subject-prefix "telemetry.sensors" --capabilities publish

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
nats_context: stone-local      # optional; empty = nats-cli default context
workspace: /home/me/my-workspace
```

## NATS

`stone` reuses the user's existing `nats` cli contexts via
[orbit.go's natscontext](https://github.com/synadia-io/orbit.go) module.

There are two ways to wire it up:

1. **Per-org sync (recommended).** Set `nats_url` on the stone context once
   (via `--nats-url` on `context create` or `org switch`). Then `stone org
   switch <org>` looks up your membership, reads the linked `nats_user`'s
   `creds_file`, writes a fresh `~/.config/stone/creds/stone-<ctx>-<org>.creds`,
   and writes a matching `~/.config/nats/context/stone-<ctx>-<org>.json`.
   The stone context's `nats_context` field is updated to point at it.
   Pass `--set-nats-default` to also update the nats-cli default context.

2. **Manual.** Set `nats_context` in the stone context yourself, pointing
   at any `nats context add` you've already created. JetStream domain (if any)
   is honored automatically.

Use `stone nats sync-context` to re-issue the context after rotating keys
(e.g., after `stone nats-user update <id> --regenerate true`).

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

## Pull / apply

`stone pull` writes one YAML file per record into `<workspace>/<collection>/`.
Org-scoped collections are filtered to the current organization.

`stone apply` walks the workspace (or the paths you pass), groups records into
batches of up to 50, and POSTs them through PocketBase's transactional `/api/batch`
endpoint. Records with an `id` are PATCHed; records without are POSTed and the
returned id is written back into the file. Apply is safe to re-run.

For diff/status/history, put the workspace in `git`.

## Entities supported by typed CRUD

Full CRUD (`ls / create / update / delete / edit`):

- Domain: `thing`, `location`, `thing-type`, `thing-type-operation`, `message-schema`
- Admin: `organization`, `membership`, `invite`
- NATS: `nats-user`, `nats-role`, `nats-import`, `nats-export`
- Nebula: `nebula-network`, `nebula-host`

Limited CRUD (`ls / update / edit` only — auto-provisioned by the platform):

- `nats-account`, `nebula-ca`

`edit <id>` opens `$EDITOR` with the record as YAML and PATCHes on save.

## JetStream admin (`stone js`)

```sh
# Streams
./stone js stream ls
./stone js stream info <name>
./stone js stream create twins --subject "twins.>" --max-age 24h --storage file
./stone js stream create twins --config stream.yaml      # advanced config
./stone js stream purge <name>
./stone js stream delete <name>

# KV buckets (lifecycle only; use 'stone kv' for data ops)
./stone js bucket ls
./stone js bucket info <name>
./stone js bucket create twins --history 5 --ttl 720h
./stone js bucket delete <name>
```

## Limitations

- Relation flags take 15-char PocketBase ids only. Discover them via
  `stone <type> ls` and copy.
- Apply does not delete server records that are missing locally. Use the
  web UI or `stone <type> delete` for that.
- No JetStream consumer management — the `nats` CLI is better at that.
- `nats-account` and `nebula-ca` updates that touch limits or infrastructure
  fields require operator-level credentials server-side. Org admins can only
  trigger key/CA rotation (`rotate_keys: true`).

## Development

```sh
go build ./...
go vet ./...
```

Module path: `github.com/stone-age-io/stone-cli`. Go 1.25+.
