package cmd

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// The CLI's field table is hand-maintained rather than generated from the
// platform's schema.json, and README/docs both say so. What neither said is how
// you would find out it had fallen behind: PocketBase silently discards writes
// to fields a collection does not have, so a flag for a field the platform
// removed keeps reporting success and doing nothing. That has already happened
// once here -- `nebula-ca --rotate-keys` existed for a while against a
// collection with no such field (see the comment on the nebula-ca spec).
//
// So this test compares the table against a vendored copy of the platform's
// schema. Vendored, not fetched: the drift being guarded against is exactly the
// kind you want to see in a diff and approve, and a stale copy fails loudly the
// same way a missing one would. Refresh it with
//
//	cp ../platform/schema.json cmd/testdata/schema.json
//
// and read cmd/testdata/schema-source.txt for which platform version it came
// from.

const (
	vendoredSchema = "testdata/schema.json"
	refreshHint    = "if the platform changed, refresh with: cp ../platform/schema.json " + vendoredSchema
)

type schemaCollection struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Fields []struct {
		Name   string `json:"name"`
		Type   string `json:"type"`
		System bool   `json:"system"`
	} `json:"fields"`
}

// neverExposed are fields no entity spec should carry a flag for, whatever the
// collection: PocketBase internals, server-managed timestamps, and the tenancy
// column the CLI injects itself from the active organization.
var neverExposed = map[string]bool{
	"id":              true,
	"created":         true,
	"updated":         true,
	"organization":    true, // injected from the active org, never typed by a user
	"tokenKey":        true, // auth internals
	"emailVisibility": true,
	"verified":        true,
}

// deliberatelyOmitted records, per collection, the fields the CLI can see and
// chooses not to expose -- with the reason, because "why is there no flag for
// this" is the question this list exists to answer. A platform field that is
// neither exposed nor listed here fails the test, which is the point: that is
// what drift looks like on the day it happens.
var deliberatelyOmitted = map[string][]string{
	// A file upload. The CLI has no multipart path, so floorplans and logos are
	// console-only; docs say as much.
	"locations":     {"floorplan"},
	"organizations": {"logo", "is_system_org", "is_operator_org"},

	// Secret material and server-derived state. The credential itself is
	// readable by the identity that owns it, but nothing types it in.
	"nats_users":   {"public_key", "private_key", "seed", "jwt", "creds_file", "active"},
	"nebula_hosts": {"certificate", "private_key", "ca_certificate", "config_yaml", "expires_at"},
	"nebula_ca":    {"certificate", "private_key"},

	// Account keys are operator-level, and the two signing-key triggers are a
	// route (POST /api/org/nats-account/keys), not fields a tenant writes.
	// rotate_keys is listed because pb-nats watches it, not because the CLI
	// should offer it.
	"nats_accounts": {
		"public_key", "private_key", "seed",
		"signing_public_key", "signing_private_key", "signing_seed",
		"signing_keys", "signing_keys_private",
		"add_signing_key", "remove_signing_key", "rotate_keys",
		"jwt", "active", "revocations",
	},
}

// libraryOwnedFields are fields the pb-* libraries create on the collections
// they own, which the platform's schema.json does not declare -- so they are
// absent from the vendored copy while being present on a live database.
//
// pb-nats creates nats_roles with these three (internal/collections/manager.go,
// createRolesCollection) and reads them back when generating a user JWT
// (internal/types/converters.go). The platform's schema.json is a dump that
// predates them, and its import is additive, so it neither declares nor removes
// them.
//
// Worth knowing rather than just working around: createRolesCollection returns
// early when the collection already exists, so a database created before those
// fields were added never acquires them, and schema.json will not add them
// either. This is the same shape as the system_account_id bug the platform fixed
// in v0.2.0. Flagged upstream; the CLI's flags are correct against a current
// database.
var libraryOwnedFields = map[string][]string{
	"nats_roles": {"allow_response", "allow_response_max", "allow_response_ttl"},
}

func loadVendoredSchema(t *testing.T) map[string]schemaCollection {
	t.Helper()

	raw, err := os.ReadFile(vendoredSchema)
	if err != nil {
		t.Fatalf("reading %s: %v\n%s", vendoredSchema, err, refreshHint)
	}

	var collections []schemaCollection
	if err := json.Unmarshal(raw, &collections); err != nil {
		t.Fatalf("parsing %s: %v", vendoredSchema, err)
	}

	byName := make(map[string]schemaCollection, len(collections))
	for _, c := range collections {
		byName[c.Name] = c
	}
	return byName
}

func TestSpecFieldsExistInThePlatformSchema(t *testing.T) {
	schema := loadVendoredSchema(t)

	for _, spec := range entitySpecs {
		t.Run(spec.Name, func(t *testing.T) {
			collection, ok := schema[spec.Collection]
			if !ok {
				t.Fatalf("collection %q is not in the platform schema: every command for this entity would 404.\n%s",
					spec.Collection, refreshHint)
			}

			known := map[string]bool{}
			for _, f := range collection.Fields {
				known[f.Name] = true
			}
			for _, f := range libraryOwnedFields[spec.Collection] {
				known[f] = true
			}

			for _, f := range spec.Fields {
				if !known[f.Name] {
					t.Errorf("field %q has a --%s flag but the collection has no such field: PocketBase discards the write and the command reports success.\n%s",
						f.Name, f.flagName(), refreshHint)
				}
			}

			// A column the collection does not have renders empty in every `ls`
			// forever, with nothing to indicate why.
			for _, col := range spec.KeyColumns {
				if !known[col] {
					t.Errorf("KeyColumns names %q, which the collection does not have: the column would always render empty.\n%s", col, refreshHint)
				}
			}

			// resolveRecordID filters on this; a field that is not there makes
			// every natural-key lookup an opaque 400.
			if spec.LookupKey != "" && !known[spec.LookupKey] {
				t.Errorf("LookupKey %q is not a field on the collection.\n%s", spec.LookupKey, refreshHint)
			}
		})
	}
}

// The other direction, and the one that actually catches the CLI lagging a
// platform release: a field the platform has that the CLI neither exposes nor
// records a reason for.
func TestPlatformFieldsAreEitherExposedOrExplained(t *testing.T) {
	schema := loadVendoredSchema(t)

	for _, spec := range entitySpecs {
		t.Run(spec.Name, func(t *testing.T) {
			collection, ok := schema[spec.Collection]
			if !ok {
				t.Skipf("collection %q missing from the vendored schema; covered by the test above", spec.Collection)
			}

			// A field counts as accounted for if the CLI can write it, shows it
			// as a column (read-only on purpose, e.g. an expiry the server
			// computes), or is listed as omitted.
			accounted := map[string]bool{}
			for _, f := range spec.Fields {
				accounted[f.Name] = true
			}
			for _, c := range spec.KeyColumns {
				accounted[c] = true
			}
			for _, f := range deliberatelyOmitted[spec.Collection] {
				accounted[f] = true
			}

			var unexplained []string
			for _, f := range collection.Fields {
				if neverExposed[f.Name] || accounted[f.Name] {
					continue
				}
				unexplained = append(unexplained, f.Name+" ("+f.Type+")")
			}

			if len(unexplained) > 0 {
				sort.Strings(unexplained)
				t.Errorf("the platform has fields this CLI neither exposes nor explains: %s\n"+
					"Either add them to the %s spec, or add them to deliberatelyOmitted[%q] with the reason.",
					strings.Join(unexplained, ", "), spec.Name, spec.Collection)
			}
		})
	}
}

// Guards the two lists above against rotting in the opposite direction: an
// entry for a field that no longer exists, or one the CLI has since started
// exposing, is stale bookkeeping that hides the next real drift.
func TestOmissionListsHaveNoStaleEntries(t *testing.T) {
	schema := loadVendoredSchema(t)

	specByCollection := map[string]EntitySpec{}
	for _, s := range entitySpecs {
		specByCollection[s.Collection] = s
	}

	for collection, omitted := range deliberatelyOmitted {
		t.Run(collection, func(t *testing.T) {
			c, ok := schema[collection]
			if !ok {
				t.Fatalf("deliberatelyOmitted names collection %q, which is not in the schema", collection)
			}
			known := map[string]bool{}
			for _, f := range c.Fields {
				known[f.Name] = true
			}

			spec := specByCollection[collection]
			for _, f := range omitted {
				if !known[f] {
					t.Errorf("listed as omitted but the platform no longer has it: %q -- drop the entry", f)
				}
				if spec.field(f) != nil {
					t.Errorf("listed as omitted but the spec exposes it: %q -- drop the entry", f)
				}
			}
		})
	}

	for collection, owned := range libraryOwnedFields {
		t.Run(collection+"/library-owned", func(t *testing.T) {
			c, ok := schema[collection]
			if !ok {
				t.Fatalf("libraryOwnedFields names collection %q, which is not in the schema", collection)
			}
			for _, f := range owned {
				for _, sf := range c.Fields {
					if sf.Name == f {
						t.Errorf("%q is now declared in the platform's schema.json, so it is no longer library-owned: drop the entry", f)
					}
				}
			}
		})
	}
}

// The vendored file is data the tests above trust completely, so a truncated or
// half-copied schema should fail as itself rather than as twenty confusing
// field errors.
func TestVendoredSchemaLooksComplete(t *testing.T) {
	schema := loadVendoredSchema(t)

	// Collections the CLI drives, plus ones it deliberately does not, as a
	// shape check on the file rather than a check on the CLI.
	for _, want := range []string{"things", "locations", "organizations", "memberships", "nats_users", "nebula_hosts", "leaf_nodes", "audit_logs"} {
		if _, ok := schema[want]; !ok {
			t.Errorf("collection %q missing: the vendored schema looks partial.\n%s", want, refreshHint)
		}
	}

	for name, c := range schema {
		if len(c.Fields) == 0 {
			t.Errorf("collection %q has no fields, which no real collection does", name)
		}
	}

	if _, err := os.Stat("testdata/schema-source.txt"); err != nil {
		t.Errorf("testdata/schema-source.txt is missing: nothing records which platform version the vendored schema came from")
	}
}
