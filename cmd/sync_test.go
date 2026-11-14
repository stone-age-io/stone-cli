package cmd

import (
	"regexp"
	"strings"
	"testing"
)

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

// edgeTriggered lists the `active` flags the platform ACTS on. Apply sends back
// every key in a file, so a pulled `true` applied after an admin set `false`
// undoes the decision: it reactivated a decommissioned device and handed it
// working credentials, until things.active joined the list. Spelled out
// separately from workspaceOmit so that deleting a line there fails here.
var edgeTriggered = map[string]string{
	"things.active":        "reactivates a decommissioned device and re-enables its credentials",
	"nats_users.active":    "re-mints a suspended NATS identity",
	"nebula_hosts.active":  "re-issues a revoked host's certificate",
	"nats_accounts.active": "puts a suspended account back on the NATS server",
	"organizations.active": "lifts an organization's suspension",
}

func TestEdgeTriggeredFlagsAreNeverPulled(t *testing.T) {
	for qualified, why := range edgeTriggered {
		collection, field := split2(qualified, '.')
		omitted := false
		for _, f := range workspaceOmit[collection] {
			if f == field {
				omitted = true
				break
			}
		}
		if !omitted {
			t.Errorf("workspaceOmit[%q] does not drop %q, so a stale workspace file %s on the next apply",
				collection, field, why)
		}
	}
}

// The filter runs on whatever the server sent, so an entry for a field that no
// longer exists is harmless at runtime and misleading to read. The vendored
// schema comes from a live server, so it includes the fields pb-nats and
// pb-nebula add at boot.
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

// nats_users.active is a writable flag AND a field pull never writes, and both
// halves are load-bearing. The flag is the only suspend (revoke hands back a
// working replacement). The omission is because pb-nats acts on the EDGE: a
// pulled `active: true` re-applied after an admin suspended the identity would
// silently un-suspend it. Exposing the flag must not drag it into workspaces.
func TestNatsUserActiveIsAFlagButNeverPulled(t *testing.T) {
	var exposed bool
	for _, spec := range entitySpecs {
		if spec.Collection != "nats_users" {
			continue
		}
		for _, f := range spec.Fields {
			if f.Name == "active" && f.Type == FBool {
				exposed = true
			}
		}
	}
	if !exposed {
		t.Error("nats-user has no bool `active` flag: there is no way to suspend an identity from the CLI")
	}

	var omitted bool
	for _, f := range workspaceOmit["nats_users"] {
		if f == "active" {
			omitted = true
		}
	}
	if !omitted {
		t.Error("workspaceOmit[nats_users] does not drop `active`: a pulled value re-applied would suspend or un-suspend an identity")
	}
}

// pull leaves out a collection only an operator can update, because apply could
// not write it back for anyone else. The list is hand-kept, so it is checked
// against the rules in the vendored schema: a syncable collection whose
// updateRule is "operator, and nothing else" must be on it, and nothing else
// may be.
func TestOperatorOnlyUpdateMatchesTheSchema(t *testing.T) {
	schema := loadVendoredSchema(t)
	comment := regexp.MustCompile(`//.*`) // a rule comment runs to the end of its line

	for _, spec := range entitySpecs {
		if !spec.syncable() {
			continue
		}
		c, ok := schema[spec.Collection]
		if !ok || c.UpdateRule == nil {
			continue
		}
		rule := strings.Join(strings.Fields(comment.ReplaceAllString(*c.UpdateRule, "")), " ")
		want := strings.HasPrefix(rule, "@request.auth.is_operator = true") && !strings.Contains(rule, "||")
		if got := operatorOnlyUpdate[spec.Collection]; got != want {
			t.Errorf("%s: operatorOnlyUpdate = %v, but its updateRule says %v: %q", spec.Collection, got, want, rule)
		}
	}
}

// memberships is not OrgScoped, but its listRule also returns the caller's own
// memberships in other organizations, which the caller cannot update from this
// one. pull must filter it to the current organization like the rest.
func TestPullScopesMembershipsToTheCurrentOrganization(t *testing.T) {
	spec, ok := specByCollection("memberships")
	if !ok {
		t.Fatal("no memberships spec")
	}
	if !pullScopedToOrg(spec) {
		t.Error("pull does not filter memberships to the current organization: an Owner's membership in another org lands in the workspace and fails on apply")
	}
}
