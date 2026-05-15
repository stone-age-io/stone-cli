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
./stone context create local --url http://localhost:8090

# 3) Log in
./stone auth login          # prompts for email + password
./stone auth whoami

# 4) Pick an organization (mirrors users.current_organization on the server)
./stone org ls
./stone org switch "System"

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
Tell `stone` which one to use by setting `nats_context` in the active context,
or leave it empty to use the nats-cli default. JetStream domain (if any) is
honored automatically.

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
