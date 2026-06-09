package cmd

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/stone-age-io/stone-cli/internal/pb"
	"gopkg.in/yaml.v3"
)

// generatePassword returns a 32-char URL-safe random password (24 bytes of
// crypto/rand, base64-encoded without padding). Comfortably above PB's
// 8-char minimum.
func generatePassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// FieldType is the typed flag value for an entity field.
type FieldType string

const (
	FString  FieldType = "string"
	FInt     FieldType = "int"
	FBool    FieldType = "bool"
	FJSON    FieldType = "json"        // accepts inline JSON, @file, or - (stdin)
	FID      FieldType = "id"          // a single PocketBase relation id
	FIDs     FieldType = "ids"         // a list of PocketBase relation ids (multi-relation)
	FSelect  FieldType = "select"      // one of Values
	FMSelect FieldType = "multiselect" // subset of Values
)

// Field declares one flag for an entity.
type Field struct {
	Name     string // JSON key on the PB record
	Flag     string // kebab-case CLI flag (default: Name with _ → -)
	Type     FieldType
	Required bool // required on create
	Help     string
	Values   []string // for select/multiselect
}

// EntitySpec defines a CRUD-able entity.
type EntitySpec struct {
	Name       string   // e.g. "thing"
	Plural     string   // e.g. "things"
	Collection string   // PocketBase collection name
	OrgScoped  bool     // auto-inject organization on create
	KeyColumns []string // table columns besides id
	Verbs      []string // empty = all of {ls, get, create, update, delete, edit}; otherwise the subset
	LookupKey  string   // record field accepted in place of an id on get/update/delete/edit ("" = id only)
	Fields     []Field
}

func (s EntitySpec) hasVerb(v string) bool {
	if len(s.Verbs) == 0 {
		return true
	}
	for _, w := range s.Verbs {
		if w == v {
			return true
		}
	}
	return false
}

// aliases returns alternate command names so users can type the singular,
// plural, hyphen, underscore, or collection form interchangeably.
// E.g. "nats-user" accepts: nats-users, nats_users, nats_user.
func (s EntitySpec) aliases() []string {
	cands := []string{
		s.Plural,
		s.Collection,
		strings.ReplaceAll(s.Name, "-", "_"),
		strings.ReplaceAll(s.Plural, "-", "_"),
		strings.ReplaceAll(s.Collection, "_", "-"),
	}
	seen := map[string]bool{s.Name: true, "": true}
	var out []string
	for _, c := range cands {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

func (s EntitySpec) field(name string) *Field {
	for i := range s.Fields {
		if s.Fields[i].Name == name {
			return &s.Fields[i]
		}
	}
	return nil
}

func (f Field) flagName() string {
	if f.Flag != "" {
		return f.Flag
	}
	return strings.ReplaceAll(f.Name, "_", "-")
}

// entitySpecs holds the first-class entity definitions. registerCRUD turns
// each into a command tree; pull/apply enumerate the same list.
var entitySpecs = []EntitySpec{
	{
		Name:       "thing",
		Plural:     "things",
		Collection: "things",
		OrgScoped:  true,
		KeyColumns: []string{"code", "name", "type", "location"},
		LookupKey:  "code",
		Fields: []Field{
			{Name: "email", Type: FString, Required: true, Help: "thing's auth email (required by the things auth collection)"},
			{Name: "password", Type: FString, Required: true, Help: "thing's auth password (min 8 chars)"},
			{Name: "name", Type: FString, Help: "display name"},
			{Name: "code", Type: FString, Help: "stable short code"},
			{Name: "description", Type: FString, Help: "free-form description"},
			{Name: "type", Type: FID, Help: "thing_types id"},
			{Name: "location", Type: FID, Help: "locations id"},
			{Name: "metadata", Type: FJSON, Help: "arbitrary JSON metadata"},
			{Name: "floorplan_position", Type: FJSON, Help: "floorplan placement as JSON (e.g. {\"x\":12,\"y\":34})"},
			{Name: "nats_user", Type: FID, Help: "nats_users id"},
			{Name: "nebula_host", Type: FID, Help: "nebula_hosts id"},
		},
	},
	{
		Name:       "location",
		Plural:     "locations",
		Collection: "locations",
		OrgScoped:  true,
		KeyColumns: []string{"code", "name", "type", "parent"},
		LookupKey:  "code",
		Fields: []Field{
			{Name: "name", Type: FString, Help: "display name"},
			{Name: "code", Type: FString, Help: "stable short code"},
			{Name: "description", Type: FString, Help: "free-form description"},
			{Name: "type", Type: FID, Help: "location_types id"},
			{Name: "parent", Type: FID, Help: "parent locations id (for hierarchy)"},
			{Name: "coordinates", Type: FJSON, Help: `geo point as JSON: {"lat":<num>,"lon":<num>}`},
			{Name: "metadata", Type: FJSON, Help: "arbitrary JSON metadata"},
		},
	},
	{
		Name:       "location-type",
		Plural:     "location-types",
		Collection: "location_types",
		OrgScoped:  true,
		KeyColumns: []string{"code", "name", "description"},
		LookupKey:  "code",
		Fields: []Field{
			{Name: "name", Type: FString, Help: "display name"},
			{Name: "code", Type: FString, Help: "stable short code"},
			{Name: "description", Type: FString, Help: "free-form description"},
		},
	},
	{
		Name:       "thing-type",
		Plural:     "thing-types",
		Collection: "thing_types",
		OrgScoped:  true,
		KeyColumns: []string{"code", "name", "capabilities", "subject_prefix"},
		LookupKey:  "code",
		Fields: []Field{
			{Name: "name", Type: FString, Help: "display name"},
			{Name: "code", Type: FString, Help: "stable short code"},
			{Name: "description", Type: FString, Help: "free-form description"},
			{Name: "capabilities", Type: FMSelect, Values: []string{"publish", "subscribe", "request", "reply"}, Help: "NATS capabilities this type uses"},
			{Name: "subject_prefix", Type: FString, Help: "subject prefix template"},
			{Name: "operations", Type: FIDs, Help: "thing_type_operations ids (comma-separated or repeat flag)"},
		},
	},
	{
		Name:       "thing-type-operation",
		Plural:     "thing-type-operations",
		Collection: "thing_type_operations",
		OrgScoped:  true,
		KeyColumns: []string{"name", "capability", "subject_suffix", "schema"},
		LookupKey:  "name",
		Fields: []Field{
			{Name: "name", Type: FString, Required: true, Help: "operation name (lowercase letters, digits, underscore)"},
			{Name: "capability", Type: FSelect, Values: []string{"publish", "subscribe", "request", "reply"}, Required: true, Help: "NATS capability"},
			{Name: "subject_suffix", Type: FString, Required: true, Help: "subject suffix appended to subject_prefix"},
			{Name: "description", Type: FString, Help: "free-form description"},
			{Name: "schema", Type: FID, Help: "message_schemas id"},
		},
	},
	{
		Name:       "message-schema",
		Plural:     "message-schemas",
		Collection: "message_schemas",
		OrgScoped:  true,
		KeyColumns: []string{"namespace", "name", "version", "format"},
		LookupKey:  "name",
		Fields: []Field{
			{Name: "namespace", Type: FString, Required: true, Help: "schema namespace (lowercase letters, digits, underscore)"},
			{Name: "name", Type: FString, Required: true, Help: "schema name"},
			{Name: "version", Type: FString, Required: true, Help: "semver: MAJOR.MINOR.PATCH"},
			{Name: "format", Type: FSelect, Values: []string{"json_schema"}, Required: true, Help: "schema format"},
			{Name: "schema", Type: FJSON, Required: true, Help: "the schema document (inline JSON, @file, or -)"},
			{Name: "description", Type: FString, Help: "free-form description"},
		},
	},
	{
		Name:       "organization",
		Plural:     "organizations",
		Collection: "organizations",
		OrgScoped:  false,
		KeyColumns: []string{"name", "active", "owner"},
		LookupKey:  "name",
		Fields: []Field{
			{Name: "name", Type: FString, Required: true, Help: "organization name (must be unique)"},
			{Name: "description", Type: FString, Help: "free-form description"},
			{Name: "active", Type: FBool, Help: "whether the organization is active"},
			{Name: "owner", Type: FID, Required: true, Help: "owning users id"},
		},
	},
	{
		Name:       "membership",
		Plural:     "memberships",
		Collection: "memberships",
		OrgScoped:  false,
		KeyColumns: []string{"user", "organization", "role"},
		Fields: []Field{
			{Name: "user", Type: FID, Required: true, Help: "users id"},
			{Name: "organization", Type: FID, Required: true, Help: "organizations id"},
			{Name: "role", Type: FSelect, Values: []string{"owner", "admin", "member", "badge"}, Required: true, Help: "membership role"},
			{Name: "invited_by", Type: FID, Help: "users id of inviter"},
			{Name: "nats_user", Type: FID, Help: "nats_users id linked to this membership"},
		},
	},
	{
		Name:       "invite",
		Plural:     "invites",
		Collection: "invites",
		OrgScoped:  true,
		KeyColumns: []string{"email", "role", "expires_at"},
		LookupKey:  "email",
		Fields: []Field{
			{Name: "email", Type: FString, Required: true, Help: "invitee email"},
			{Name: "role", Type: FSelect, Values: []string{"admin", "member", "badge"}, Required: true, Help: "membership role to grant"},
			{Name: "token", Type: FString, Help: "invite token (auto-generated if blank)"},
			{Name: "expires_at", Type: FString, Help: "RFC 3339 expiry timestamp"},
			{Name: "resend_invite", Type: FBool, Help: "set true to trigger resend"},
			{Name: "invited_by", Type: FID, Help: "users id of inviter (defaults to current user server-side)"},
		},
	},

	// ---- NATS collections ------------------------------------------------

	{
		Name:       "nats-user",
		Plural:     "nats-users",
		Collection: "nats_users",
		OrgScoped:  true,
		KeyColumns: []string{"nats_username", "account_id", "role_id", "active", "bearer_token"},
		LookupKey:  "nats_username",
		Fields: []Field{
			{Name: "email", Type: FString, Required: true, Help: "auth email (required by the auth collection)"},
			{Name: "password", Type: FString, Required: true, Help: "auth password (min 8 chars)"},
			{Name: "nats_username", Type: FString, Required: true, Help: "NATS username"},
			{Name: "description", Type: FString, Help: "free-form description"},
			{Name: "account_id", Type: FID, Required: true, Help: "nats_accounts id"},
			{Name: "role_id", Type: FID, Required: true, Help: "nats_roles id"},
			{Name: "bearer_token", Type: FBool, Help: "issue as bearer-token user (no signing)"},
			{Name: "active", Type: FBool, Help: "whether the user is active"},
			{Name: "jwt_expires_at", Type: FString, Help: `credential expiry, RFC 3339 (pass "" to clear = never expires)`},
			{Name: "publish_permissions", Type: FJSON, Help: "per-user publish allow rules (JSON array of subjects; @file or - accepted)"},
			{Name: "subscribe_permissions", Type: FJSON, Help: "per-user subscribe allow rules (JSON array of subjects)"},
			{Name: "publish_deny_permissions", Type: FJSON, Help: "per-user publish deny rules (JSON array of subjects)"},
			{Name: "subscribe_deny_permissions", Type: FJSON, Help: "per-user subscribe deny rules (JSON array of subjects)"},
			{Name: "regenerate", Type: FBool, Help: "set true to trigger key rotation"},
		},
	},
	{
		Name:       "nats-role",
		Plural:     "nats-roles",
		Collection: "nats_roles",
		OrgScoped:  true,
		KeyColumns: []string{"name", "is_default", "max_subscriptions", "max_payload"},
		LookupKey:  "name",
		Fields: []Field{
			{Name: "name", Type: FString, Required: true, Help: "role name (unique per org)"},
			{Name: "description", Type: FString, Help: "free-form description"},
			{Name: "is_default", Type: FBool, Help: "use as default role for new NATS users"},
			{Name: "max_subscriptions", Type: FInt, Help: "max concurrent subscriptions (-1 = unlimited)"},
			{Name: "max_data", Type: FInt, Help: "max bytes in flight (-1 = unlimited)"},
			{Name: "max_payload", Type: FInt, Help: "max message payload bytes (-1 = unlimited)"},
			{Name: "allow_response", Type: FBool, Help: "allow publishing responses to reply subjects of received requests"},
			{Name: "allow_response_max", Type: FInt, Help: "max allowed responses per request (0 = server default)"},
			{Name: "allow_response_ttl", Type: FInt, Help: "response permission expiry in nanoseconds (Go duration; 0 = none)"},
			{Name: "publish_permissions", Type: FJSON, Help: "publish allow rules (JSON; @file or - accepted)"},
			{Name: "subscribe_permissions", Type: FJSON, Help: "subscribe allow rules (JSON)"},
			{Name: "publish_deny_permissions", Type: FJSON, Help: "publish deny rules (JSON)"},
			{Name: "subscribe_deny_permissions", Type: FJSON, Help: "subscribe deny rules (JSON)"},
		},
	},
	{
		Name:       "nats-account",
		Plural:     "nats-accounts",
		Collection: "nats_accounts",
		OrgScoped:  true,
		Verbs:      []string{"ls", "get", "update", "edit"},
		KeyColumns: []string{"name", "max_connections", "max_subscriptions", "max_payload"},
		LookupKey:  "name",
		Fields: []Field{
			{Name: "name", Type: FString, Help: "account name"},
			{Name: "description", Type: FString, Help: "free-form description"},
			{Name: "max_connections", Type: FInt, Help: "max concurrent connections (operator-only)"},
			{Name: "max_subscriptions", Type: FInt, Help: "max concurrent subscriptions (operator-only)"},
			{Name: "max_data", Type: FInt, Help: "max data in flight (operator-only)"},
			{Name: "max_payload", Type: FInt, Help: "max message payload bytes (operator-only)"},
			{Name: "max_jetstream_disk_storage", Type: FInt, Help: "JS disk quota (operator-only)"},
			{Name: "max_jetstream_memory_storage", Type: FInt, Help: "JS memory quota (operator-only)"},
			{Name: "rotate_keys", Type: FBool, Help: "set true to trigger key rotation"},
		},
	},
	{
		Name:       "nats-import",
		Plural:     "nats-imports",
		Collection: "nats_account_imports",
		OrgScoped:  true,
		KeyColumns: []string{"name", "type", "subject", "local_subject"},
		LookupKey:  "name",
		Fields: []Field{
			{Name: "account_id", Type: FID, Required: true, Help: "local nats_accounts id"},
			{Name: "name", Type: FString, Required: true, Help: "import name"},
			{Name: "subject", Type: FString, Required: true, Help: "remote subject to import"},
			{Name: "account", Type: FString, Required: true, Help: "remote account public key"},
			{Name: "type", Type: FSelect, Values: []string{"stream", "service"}, Required: true, Help: "import kind"},
			{Name: "token", Type: FString, Help: "activation token (required for private exports)"},
			{Name: "local_subject", Type: FString, Help: "local subject mapping"},
			{Name: "share", Type: FBool, Help: "share account info with the exporting account"},
			{Name: "allow_trace", Type: FBool, Help: "allow distributed tracing for this import"},
			{Name: "description", Type: FString, Help: "free-form description"},
		},
	},
	{
		Name:       "nats-export",
		Plural:     "nats-exports",
		Collection: "nats_account_exports",
		OrgScoped:  true,
		KeyColumns: []string{"name", "type", "subject", "token_req"},
		LookupKey:  "name",
		Fields: []Field{
			{Name: "account_id", Type: FID, Required: true, Help: "local nats_accounts id"},
			{Name: "name", Type: FString, Required: true, Help: "export name"},
			{Name: "subject", Type: FString, Required: true, Help: "subject to expose"},
			{Name: "type", Type: FSelect, Values: []string{"stream", "service"}, Required: true, Help: "export kind"},
			{Name: "token_req", Type: FBool, Help: "require activation tokens from importing accounts"},
			{Name: "response_type", Type: FSelect, Values: []string{"Singleton", "Stream", "Chunked"}, Help: "response type (service exports only)"},
			{Name: "response_threshold", Type: FInt, Help: "response threshold in ns (service exports only)"},
			{Name: "account_token_position", Type: FInt, Help: "wildcard position bound by the activation token"},
			{Name: "advertise", Type: FBool, Help: "advertise this export to the registry"},
			{Name: "allow_trace", Type: FBool, Help: "allow distributed tracing for this export"},
			{Name: "description", Type: FString, Help: "free-form description"},
		},
	},

	// ---- Nebula collections ----------------------------------------------

	{
		Name:       "nebula-network",
		Plural:     "nebula-networks",
		Collection: "nebula_networks",
		OrgScoped:  true,
		KeyColumns: []string{"name", "cidr_range", "active", "ca_id"},
		LookupKey:  "name",
		Fields: []Field{
			{Name: "name", Type: FString, Required: true, Help: "network name"},
			{Name: "description", Type: FString, Help: "free-form description"},
			{Name: "cidr_range", Type: FString, Required: true, Help: "CIDR e.g. 192.168.100.0/24"},
			{Name: "active", Type: FBool, Help: "whether the network is active"},
			{Name: "ca_id", Type: FID, Required: true, Help: "nebula_ca id"},
		},
	},
	{
		Name:       "nebula-host",
		Plural:     "nebula-hosts",
		Collection: "nebula_hosts",
		OrgScoped:  true,
		KeyColumns: []string{"hostname", "overlay_ip", "network_id", "is_lighthouse", "active"},
		LookupKey:  "hostname",
		Fields: []Field{
			{Name: "email", Type: FString, Required: true, Help: "auth email (required by the auth collection)"},
			{Name: "password", Type: FString, Required: true, Help: "auth password (min 8 chars)"},
			{Name: "hostname", Type: FString, Required: true, Help: "host hostname (unique)"},
			{Name: "overlay_ip", Type: FString, Required: true, Help: "Nebula overlay IP within the network's CIDR"},
			{Name: "network_id", Type: FID, Required: true, Help: "nebula_networks id"},
			{Name: "groups", Type: FJSON, Help: "Nebula firewall groups (JSON array)"},
			{Name: "is_lighthouse", Type: FBool, Help: "whether this host is a lighthouse"},
			{Name: "public_host_port", Type: FString, Help: "public address:port (for lighthouses / static peers)"},
			{Name: "firewall_outbound", Type: FJSON, Help: "outbound firewall rules (JSON)"},
			{Name: "firewall_inbound", Type: FJSON, Help: "inbound firewall rules (JSON)"},
			{Name: "validity_years", Type: FInt, Help: "certificate validity in years"},
			{Name: "active", Type: FBool, Help: "whether the host is active"},
		},
	},
	{
		Name:       "nebula-ca",
		Plural:     "nebula-cas",
		Collection: "nebula_ca",
		OrgScoped:  true,
		Verbs:      []string{"ls", "get", "update", "edit"},
		KeyColumns: []string{"name", "curve", "validity_years", "expires_at"},
		LookupKey:  "name",
		Fields: []Field{
			{Name: "name", Type: FString, Help: "CA name"},
			{Name: "validity_years", Type: FInt, Help: "CA cert validity in years (operator-only)"},
			{Name: "curve", Type: FString, Help: "elliptic curve, e.g. P256 (operator-only)"},
			{Name: "rotate_keys", Type: FBool, Help: "set true to trigger CA rotation"},
		},
	},

	// ---- Leaf nodes ------------------------------------------------------
	// A Leaf Node models a customer site running its own local NATS server.
	// It's an auth collection ("a special Thing") that ties together a NATS
	// identity, an optional Nebula host, and an allowlist of collections the
	// site's leaf-sync agent mirrors into local KV. See platform-docs
	// leaf-nodes.md.

	{
		Name:       "leaf-node",
		Plural:     "leaf-nodes",
		Collection: "leaf_nodes",
		OrgScoped:  true,
		KeyColumns: []string{"code", "name", "domain", "nats_user", "location"},
		LookupKey:  "code",
		Fields: []Field{
			{Name: "email", Type: FString, Required: true, Help: "leaf node's auth email (required by the leaf_nodes auth collection)"},
			{Name: "password", Type: FString, Required: true, Help: "leaf node's auth password (min 8 chars)"},
			{Name: "name", Type: FString, Help: "display name"},
			{Name: "code", Type: FString, Help: "site slug; derives the NATS username and JetStream domain (immutable after creation)"},
			{Name: "description", Type: FString, Help: "free-form description"},
			{Name: "domain", Type: FString, Help: "local JetStream domain, e.g. edge-<code> (usually derived from code)"},
			{Name: "synced_collections", Type: FMSelect, Values: []string{"things", "locations", "thing_types", "location_types", "thing_type_operations", "message_schemas"}, Help: "collections this site mirrors; subset of the sync allowlist (comma-separated)"},
			{Name: "location", Type: FID, Help: "locations id (optional site context)"},
			{Name: "metadata", Type: FJSON, Help: "arbitrary JSON metadata"},
			{Name: "nats_user", Type: FID, Help: "nats_users id (auto-provisioned by a server-side hook on create)"},
			{Name: "nebula_host", Type: FID, Help: "nebula_hosts id (optional overlay-mesh membership)"},
		},
	},
}

// resolveOutput returns the effective output format for typed CRUD commands.
func resolveOutput() string {
	if flagOutput != "" {
		return flagOutput
	}
	g, _ := ctx.LoadGlobal()
	if g.Output != "" {
		return g.Output
	}
	return "table"
}

// registerCRUD wires ls/get/create/update/delete/edit for a single entity.
func registerCRUD(spec EntitySpec) {
	root := &cobra.Command{
		Use:     spec.Name,
		Aliases: spec.aliases(),
		Short:   fmt.Sprintf("Manage %s records", spec.Plural),
	}
	if spec.hasVerb("ls") {
		root.AddCommand(buildLsCmd(spec))
	}
	if spec.hasVerb("get") {
		root.AddCommand(buildGetCmd(spec))
	}
	if spec.hasVerb("create") {
		root.AddCommand(buildCreateCmd(spec))
	}
	if spec.hasVerb("update") {
		root.AddCommand(buildUpdateCmd(spec))
	}
	if spec.hasVerb("delete") {
		root.AddCommand(buildDeleteCmd(spec))
	}
	if spec.hasVerb("edit") {
		root.AddCommand(buildEditCmd(spec))
	}
	rootCmd.AddCommand(root)
}

func buildLsCmd(spec EntitySpec) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   fmt.Sprintf("List %s", spec.Plural),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := ctx.Active(flagContext)
			if err != nil {
				return err
			}
			client := newPBClient(c)
			sort, _ := cmd.Flags().GetString("sort")
			fields, _ := cmd.Flags().GetString("fields")
			opts := pb.ListOptions{Sort: sort, Fields: fields}
			extraFilter, _ := cmd.Flags().GetString("filter")
			opts.Filter = composeOrgFilter(spec, c.CurrentOrganization, extraFilter)
			items, err := client.ListAll(spec.Collection, opts)
			if err != nil {
				return err
			}
			cols := append([]string{"id"}, spec.KeyColumns...)
			if fields != "" {
				cols = splitFields(fields)
			}
			return pb.PrintList(os.Stdout, items, cols, resolveOutput())
		},
	}
	cmd.Flags().String("filter", "", "extra PocketBase filter expression to AND with the org filter")
	cmd.Flags().String("sort", "", `PocketBase sort expression, e.g. "-updated" or "name"`)
	cmd.Flags().String("fields", "", "comma-separated fields to return (server-side projection); table columns follow it")
	return cmd
}

func buildGetCmd(spec EntitySpec) *cobra.Command {
	cmd := &cobra.Command{
		Use:     idArgUse(spec, "get"),
		Aliases: []string{"show"},
		Short:   fmt.Sprintf("Get a single %s", spec.Name),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := ctx.Active(flagContext)
			if err != nil {
				return err
			}
			client := newPBClient(c)
			id, err := resolveRecordID(client, spec, c.CurrentOrganization, args[0])
			if err != nil {
				return err
			}
			fields, _ := cmd.Flags().GetString("fields")
			r, err := client.Get(spec.Collection, id, pb.GetOptions{Fields: fields})
			if err != nil {
				return err
			}
			return pb.PrintRecord(os.Stdout, r, resolveOutput())
		},
	}
	cmd.Flags().String("fields", "", "comma-separated fields to return (server-side projection)")
	return cmd
}

// idArgUse renders the Use string for verbs taking a positional record arg.
func idArgUse(spec EntitySpec, verb string) string {
	if spec.LookupKey != "" {
		return fmt.Sprintf("%s <id|%s>", verb, spec.LookupKey)
	}
	return verb + " <id>"
}

// resolveRecordID resolves a positional <id|key> argument to a record id.
// Id-shaped args are tried as ids first; anything else — or an id-shaped arg
// that doesn't resolve — is matched exactly against the spec's LookupKey,
// scoped to the current organization for org-scoped collections.
func resolveRecordID(client *pb.Client, spec EntitySpec, orgID, arg string) (string, error) {
	idShaped := pocketbaseIDRE.MatchString(arg)
	if idShaped {
		if _, err := client.Get(spec.Collection, arg, pb.GetOptions{Fields: "id"}); err == nil {
			return arg, nil
		} else if spec.LookupKey == "" {
			return "", fmt.Errorf("%s id %q not accessible: %w", spec.Name, arg, err)
		}
	}
	if spec.LookupKey == "" {
		return "", fmt.Errorf("%s takes a 15-char PocketBase id, got %q", spec.Name, arg)
	}
	filter := composeOrgFilter(spec, orgID, fmt.Sprintf(`%s="%s"`, spec.LookupKey, escapePBString(arg)))
	items, err := client.ListAll(spec.Collection, pb.ListOptions{Filter: filter, Fields: "id"})
	if err != nil {
		return "", err
	}
	switch len(items) {
	case 0:
		if idShaped {
			return "", fmt.Errorf("no %s with id or %s %q", spec.Name, spec.LookupKey, arg)
		}
		return "", fmt.Errorf("no %s with %s %q", spec.Name, spec.LookupKey, arg)
	case 1:
		id, _ := items[0]["id"].(string)
		return id, nil
	default:
		ids := make([]string, 0, len(items))
		for _, it := range items {
			if id, ok := it["id"].(string); ok {
				ids = append(ids, id)
			}
		}
		return "", fmt.Errorf("multiple %s match %s %q (%s); use the id", spec.Plural, spec.LookupKey, arg, strings.Join(ids, ", "))
	}
}

// splitFields turns a --fields value into table column names.
func splitFields(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func buildCreateCmd(spec EntitySpec) *cobra.Command {
	hasPassword := spec.field("password") != nil
	cmd := &cobra.Command{
		Use:   "create",
		Short: fmt.Sprintf("Create a %s", spec.Name),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := ctx.Active(flagContext)
			if err != nil {
				return err
			}
			var generatedPW string
			if hasPassword {
				rnd, _ := cmd.Flags().GetBool("random-password")
				pwSet := cmd.Flags().Changed("password")
				if !rnd && !pwSet {
					return errors.New("must pass --password or --random-password")
				}
				if rnd && pwSet {
					return errors.New("cannot pass both --password and --random-password")
				}
				if rnd {
					pw, err := generatePassword()
					if err != nil {
						return fmt.Errorf("generate password: %w", err)
					}
					if err := cmd.Flags().Set("password", pw); err != nil {
						return err
					}
					generatedPW = pw
				}
			}
			data, err := collectFields(cmd, spec, true)
			if err != nil {
				return err
			}
			if spec.OrgScoped {
				if c.CurrentOrganization == "" {
					return errors.New("no current organization. run: stone org switch <name|id>")
				}
				if _, ok := data["organization"]; !ok {
					data["organization"] = c.CurrentOrganization
				}
			}
			client := newPBClient(c)
			r, err := client.Create(spec.Collection, data)
			if err != nil {
				return err
			}
			if generatedPW != "" {
				fmt.Fprintf(os.Stderr, "generated password: %s\n", generatedPW)
			}
			return pb.PrintRecord(os.Stdout, r, resolveOutput())
		},
	}
	addFieldFlags(cmd, spec, true)
	if hasPassword {
		cmd.Flags().Bool("random-password", false, "generate a strong random password and print it to stderr (mutually exclusive with --password)")
		// addFieldFlags marked --password as Cobra-required from the spec.
		// Now that --random-password is an alternative, do that check in RunE.
		if pf := cmd.Flag("password"); pf != nil {
			delete(pf.Annotations, cobra.BashCompOneRequiredFlag)
		}
	}
	return cmd
}

func buildUpdateCmd(spec EntitySpec) *cobra.Command {
	cmd := &cobra.Command{
		Use:   idArgUse(spec, "update"),
		Short: fmt.Sprintf("Update a %s", spec.Name),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := ctx.Active(flagContext)
			if err != nil {
				return err
			}
			data, err := collectFields(cmd, spec, false)
			if err != nil {
				return err
			}
			if len(data) == 0 {
				return errors.New("no fields to update; pass at least one flag")
			}
			client := newPBClient(c)
			id, err := resolveRecordID(client, spec, c.CurrentOrganization, args[0])
			if err != nil {
				return err
			}
			r, err := client.Update(spec.Collection, id, data)
			if err != nil {
				return err
			}
			return pb.PrintRecord(os.Stdout, r, resolveOutput())
		},
	}
	addFieldFlags(cmd, spec, false)
	return cmd
}

func buildDeleteCmd(spec EntitySpec) *cobra.Command {
	cmd := &cobra.Command{
		Use:     idArgUse(spec, "delete"),
		Aliases: []string{"rm"},
		Short:   fmt.Sprintf("Delete a %s", spec.Name),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := ctx.Active(flagContext)
			if err != nil {
				return err
			}
			client := newPBClient(c)
			id, err := resolveRecordID(client, spec, c.CurrentOrganization, args[0])
			if err != nil {
				return err
			}
			label := args[0]
			if id != args[0] {
				label = fmt.Sprintf("%s (%s)", args[0], id)
			}
			yes, _ := cmd.Flags().GetBool("yes")
			if !yes {
				fmt.Fprintf(os.Stderr, "delete %s %s? [y/N] ", spec.Name, label)
				r := bufio.NewReader(os.Stdin)
				line, _ := r.ReadString('\n')
				if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y") {
					return errors.New("aborted")
				}
			}
			if err := client.Delete(spec.Collection, id); err != nil {
				return err
			}
			fmt.Printf("deleted %s %s\n", spec.Name, label)
			return nil
		},
	}
	cmd.Flags().BoolP("yes", "y", false, "skip confirmation prompt")
	return cmd
}

func buildEditCmd(spec EntitySpec) *cobra.Command {
	return &cobra.Command{
		Use:   idArgUse(spec, "edit"),
		Short: fmt.Sprintf("Open a %s in $EDITOR (YAML) and apply on save", spec.Name),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := ctx.Active(flagContext)
			if err != nil {
				return err
			}
			client := newPBClient(c)
			id, err := resolveRecordID(client, spec, c.CurrentOrganization, args[0])
			if err != nil {
				return err
			}
			cur, err := client.Get(spec.Collection, id)
			if err != nil {
				return err
			}
			pb.Strip(cur)
			tmpf, err := os.CreateTemp("", fmt.Sprintf("stone-%s-*.yaml", spec.Name))
			if err != nil {
				return err
			}
			tmp := tmpf.Name()
			defer os.Remove(tmp)
			data, err := yaml.Marshal(cur)
			if err != nil {
				return err
			}
			if _, err := tmpf.Write(data); err != nil {
				tmpf.Close()
				return err
			}
			tmpf.Close()
			if err := openEditor(tmp); err != nil {
				return err
			}
			updated, err := pb.UnmarshalFile(tmp)
			if err != nil {
				return err
			}
			// Drop id from the patch body if present; the URL carries it.
			delete(updated, "id")
			r, err := client.Update(spec.Collection, id, updated)
			if err != nil {
				return err
			}
			return pb.PrintRecord(os.Stdout, r, resolveOutput())
		},
	}
}

func addFieldFlags(cmd *cobra.Command, spec EntitySpec, requireRequired bool) {
	for _, f := range spec.Fields {
		switch f.Type {
		case FString, FID, FSelect, FJSON:
			cmd.Flags().String(f.flagName(), "", f.Help)
		case FInt:
			cmd.Flags().Int(f.flagName(), 0, f.Help)
		case FBool:
			cmd.Flags().Bool(f.flagName(), false, f.Help)
		case FIDs, FMSelect:
			cmd.Flags().StringSlice(f.flagName(), nil, f.Help)
		}
		if requireRequired && f.Required {
			_ = cmd.MarkFlagRequired(f.flagName())
		}
	}
}

func collectFields(cmd *cobra.Command, spec EntitySpec, forCreate bool) (pb.Record, error) {
	out := pb.Record{}
	for _, f := range spec.Fields {
		name := f.flagName()
		if !forCreate && !cmd.Flags().Changed(name) {
			continue
		}
		switch f.Type {
		case FString:
			v, _ := cmd.Flags().GetString(name)
			if forCreate && f.Required && v == "" {
				return nil, fmt.Errorf("--%s is required", name)
			}
			if v != "" || cmd.Flags().Changed(name) {
				out[f.Name] = v
			}
		case FID:
			v, _ := cmd.Flags().GetString(name)
			if forCreate && f.Required && v == "" {
				return nil, fmt.Errorf("--%s is required", name)
			}
			if v != "" || cmd.Flags().Changed(name) {
				out[f.Name] = v
			}
		case FSelect:
			v, _ := cmd.Flags().GetString(name)
			if forCreate && f.Required && v == "" {
				return nil, fmt.Errorf("--%s is required (one of: %s)", name, strings.Join(f.Values, ", "))
			}
			if v != "" {
				if !containsString(f.Values, v) {
					return nil, fmt.Errorf("--%s must be one of: %s", name, strings.Join(f.Values, ", "))
				}
				out[f.Name] = v
			}
		case FInt:
			if cmd.Flags().Changed(name) {
				v, _ := cmd.Flags().GetInt(name)
				out[f.Name] = v
			}
		case FBool:
			if cmd.Flags().Changed(name) {
				v, _ := cmd.Flags().GetBool(name)
				out[f.Name] = v
			}
		case FIDs:
			if cmd.Flags().Changed(name) {
				v, _ := cmd.Flags().GetStringSlice(name)
				out[f.Name] = v
			}
		case FMSelect:
			if cmd.Flags().Changed(name) {
				v, _ := cmd.Flags().GetStringSlice(name)
				for _, item := range v {
					if !containsString(f.Values, item) {
						return nil, fmt.Errorf("--%s value %q is not one of: %s", name, item, strings.Join(f.Values, ", "))
					}
				}
				out[f.Name] = v
			}
		case FJSON:
			v, _ := cmd.Flags().GetString(name)
			if v == "" {
				if forCreate && f.Required {
					return nil, fmt.Errorf("--%s is required", name)
				}
				if !cmd.Flags().Changed(name) {
					continue
				}
			}
			parsed, err := readJSONValue(v)
			if err != nil {
				return nil, fmt.Errorf("--%s: %w", name, err)
			}
			out[f.Name] = parsed
		}
	}
	return out, nil
}

// readJSONValue accepts:
//   - "@path" → read file as JSON
//   - "-"     → read stdin as JSON
//   - inline  → parse as JSON
//   - ""      → returns nil (caller decided to include the field)
func readJSONValue(v string) (any, error) {
	var raw []byte
	switch {
	case v == "":
		return nil, nil
	case v == "-":
		b, err := readAll(os.Stdin)
		if err != nil {
			return nil, err
		}
		raw = b
	case strings.HasPrefix(v, "@"):
		b, err := os.ReadFile(v[1:])
		if err != nil {
			return nil, err
		}
		raw = b
	default:
		raw = []byte(v)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	return out, nil
}

func readAll(f *os.File) ([]byte, error) {
	const max = 32 * 1024 * 1024
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for len(buf) < max {
		n, err := f.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, err
		}
	}
	return buf, nil
}

func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func composeOrgFilter(spec EntitySpec, orgID, extra string) string {
	parts := []string{}
	if spec.OrgScoped && orgID != "" {
		parts = append(parts, fmt.Sprintf(`organization="%s"`, orgID))
	}
	if extra != "" {
		parts = append(parts, "("+extra+")")
	}
	return strings.Join(parts, " && ")
}

func openEditor(path string) error {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		if runtime.GOOS == "windows" {
			editor = "notepad"
		} else {
			editor = "vi"
		}
	}
	// Allow editors like "code --wait" by splitting on whitespace.
	parts := strings.Fields(editor)
	parts = append(parts, path)
	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// init registers CRUD commands for every entity spec.
func init() {
	for _, s := range entitySpecs {
		registerCRUD(s)
	}
}
