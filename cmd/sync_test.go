package cmd

import "testing"

// `stone pull` writes one YAML file per record into a directory the docs tell
// you to commit to git. What it must never write is a credential -- and it did:
// PocketBase hides private_key and seed, but it returns nats_users.creds_file
// (a user JWT and an nkey seed: the whole credential), nebula_hosts.config_yaml
// (the host's Nebula private key, inline) and invites.token (a bearer token to
// join the organization) to any owner or admin who lists the collection.
//
// workspaceOmit is the filter. These tests are why it cannot quietly lose an
// entry.

// mustOmit is the short list whose presence in a workspace file is a credential
// leak rather than a tidiness problem. Spelled out separately from workspaceOmit
// so that deleting a line there fails here.
var mustOmit = map[string]string{
	"nats_users.creds_file":      "a NATS user JWT and nkey seed -- the credential itself",
	"nebula_hosts.config_yaml":   "contains the host's Nebula private key inline (pb-nebula stores it plaintext)",
	"invites.token":              "a bearer token that redeems into a membership",
	"nats_accounts.signing_keys": "account signing key material",
}

func TestCredentialsAreNeverWrittenToTheWorkspace(t *testing.T) {
	for qualified, why := range mustOmit {
		collection, field := split2(qualified, '.')
		omitted := false
		for _, f := range workspaceOmit[collection] {
			if f == field {
				omitted = true
				break
			}
		}
		if !omitted {
			t.Errorf("workspaceOmit[%q] does not drop %q, so `stone pull` writes it to a file: %s",
				collection, field, why)
		}
	}
}

// The filter runs on whatever the server sent, so an entry for a field that no
// longer exists is harmless at runtime and misleading to read. Checked against
// the same three sources the drift tests use, because four of these fields are
// owned by pb-nebula rather than declared in the platform's schema.json.
func TestWorkspaceOmitNamesFieldsThatExist(t *testing.T) {
	schema := loadVendoredSchema(t)

	for collection, fields := range workspaceOmit {
		t.Run(collection, func(t *testing.T) {
			c, ok := schema[collection]
			if !ok {
				t.Fatalf("workspaceOmit names collection %q, which is not in the platform schema", collection)
			}
			known := map[string]bool{}
			for _, f := range c.Fields {
				known[f.Name] = true
			}
			for _, f := range libraryOwnedFields[collection] {
				known[f] = true
			}
			for f := range routeOnlyFields[collection] {
				known[f] = true
			}
			for _, f := range fields {
				if !known[f] {
					t.Errorf("omits %q, which no longer exists -- drop the entry.\n%s", f, refreshHint)
				}
			}
		})
	}
}

// pull and apply both walk entitySpecs and gate on syncable(). Getting this
// wrong in either direction is silent: a read-only collection in the workspace
// fails every apply, and a writable one dropped from it stops being reconciled
// with nothing said.
func TestSyncableTracksTheWritableEntities(t *testing.T) {
	want := map[string]bool{
		// Append-only by construction; all three write rules are nil.
		"activity": false,
		// Tenant-writable in full.
		"things":    true,
		"locations": true,
		// Update-only, and that is enough: an operator reconciles these.
		"nats_accounts": true,
		"nebula_ca":     true,
	}

	for _, spec := range entitySpecs {
		w, ok := want[spec.Collection]
		if !ok {
			continue
		}
		if got := spec.syncable(); got != w {
			t.Errorf("%s: syncable() = %v, want %v", spec.Collection, got, w)
		}
	}
}

// split2 splits "collection.field" at the first sep.
func split2(s string, sep byte) (string, string) {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}
