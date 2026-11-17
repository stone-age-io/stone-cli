package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/stone-age-io/stone-cli/internal/pb"
)

// keyServer answers a list in any collection whose filter names one of keys
// with that key's id, and nothing else. It records each filter it was sent.
func keyServer(t *testing.T, keys map[string]string) (*pb.Client, *[]string) {
	t.Helper()
	var filters []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/records") {
			http.NotFound(w, r) // a get by id: not an id here
			return
		}
		filter := r.URL.Query().Get("filter")
		filters = append(filters, filter)
		items := []pb.Record{}
		for key, id := range keys {
			if strings.Contains(filter, `"`+key+`"`) {
				items = append(items, pb.Record{"id": id})
			}
		}
		_ = json.NewEncoder(w).Encode(pb.ListResult{Page: 1, TotalPages: 1, Items: items})
	}))
	t.Cleanup(srv.Close)
	return pb.New(ctx.Context{URL: srv.URL, Auth: ctx.AuthSection{Token: "not-a-jwt"}}), &filters
}

func TestRelationFlagsTakeNaturalKeys(t *testing.T) {
	client, filters := keyServer(t, map[string]string{
		"hq":          "loc000000000001",
		"temp-sensor": "typ000000000001",
	})
	spec, _ := specByCollection("things")
	data := pb.Record{"name": "Probe", "location": "HQ", "type": "temp-sensor", "nebula_host": ""}

	if err := resolveRelationFlags(client, spec, "org000000000001", data); err != nil {
		t.Fatal(err)
	}
	if data["location"] != "loc000000000001" || data["type"] != "typ000000000001" {
		t.Errorf("got location=%v type=%v, want both resolved to ids", data["location"], data["type"])
	}
	if data["nebula_host"] != "" {
		t.Errorf("an empty flag clears the relation and must stay empty, got %v", data["nebula_host"])
	}
	// The same lookup as a positional argument: case-folded code, current org.
	for _, f := range *filters {
		if !strings.Contains(f, "org000000000001") {
			t.Errorf("lookup %q is not scoped to the current organization", f)
		}
	}
}

func TestRelationListFlagResolvesEachValue(t *testing.T) {
	client, _ := keyServer(t, map[string]string{"heartbeat": "op0000000000001", "reboot": "op0000000000002"})
	spec, _ := specByCollection("thing_types")
	data := pb.Record{"operations": []string{"heartbeat", "reboot"}}

	if err := resolveRelationFlags(client, spec, "org000000000001", data); err != nil {
		t.Fatal(err)
	}
	got := data["operations"].([]string)
	if len(got) != 2 || got[0] != "op0000000000001" || got[1] != "op0000000000002" {
		t.Errorf("got %v", got)
	}
}

func TestRelationFlagNamesTheFlagWhenNothingMatches(t *testing.T) {
	client, _ := keyServer(t, nil)
	spec, _ := specByCollection("things")
	err := resolveRelationFlags(client, spec, "org000000000001", pb.Record{"location": "nowhere"})
	if err == nil || !strings.Contains(err.Error(), "--location") {
		t.Errorf("got %v, want an error naming --location", err)
	}
}

// Everything this file pins was checked against a running platform first (see
// the header comment in relations.go). The tests exist because the behaviours
// are all silent when wrong: a mislabelled column prints a plausible-looking
// string, and a column that stops being expanded prints an id that looks like it
// always did.

func TestRelationLabelPrefersTheSpecsLookupKey(t *testing.T) {
	// thing_types is modelled, and its LookupKey is `code` -- so a thing's
	// `type` column reads temp-sensor, not "Temp Sensor" and not the id.
	got := relationLabel(map[string]any{
		"collectionName": "thing_types",
		"id":             "wjmd99c8tat1grr",
		"code":           "temp-sensor",
		"name":           "Temp Sensor",
	})
	if got != "temp-sensor" {
		t.Errorf("got %q, want the code", got)
	}
}

func TestRelationLabelFallsBackForUsers(t *testing.T) {
	// users has no EntitySpec, and email is its natural key.
	self := relationLabel(map[string]any{
		"collectionName": "users",
		"email":          "owner@example.com",
		"name":           "Ada Owner",
	})
	if self != "owner@example.com" {
		t.Errorf("got %q, want the email", self)
	}

	// PocketBase masks the email on a record that is not the caller's own
	// unless emailVisibility is set -- an owner expanding a colleague's
	// membership really does get a null email and a populated name. Falling
	// through to the name is what keeps the USER column from going blank for
	// everyone but yourself.
	colleague := relationLabel(map[string]any{
		"collectionName":  "users",
		"email":           nil,
		"emailVisibility": false,
		"name":            "Mo Member",
	})
	if colleague != "Mo Member" {
		t.Errorf("got %q, want the name once the email is masked", colleague)
	}

	// Nothing to show is an empty cell, never an id.
	if got := relationLabel(map[string]any{"collectionName": "users", "id": "abc123xyz000000"}); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestResolveRelationColumnsRewritesInPlace(t *testing.T) {
	items := []pb.Record{{
		"id":       "sii1rtrxtadt2ad",
		"code":     "sensor-42",
		"type":     "wjmd99c8tat1grr",
		"location": "8adias9mxvono9x",
		// nebula_host is unset, so the server sends no expand entry for it.
		"nebula_host": "",
		"expand": map[string]any{
			"type":     map[string]any{"collectionName": "thing_types", "code": "temp-sensor"},
			"location": map[string]any{"collectionName": "locations", "code": "hq"},
		},
	}}

	resolveRelationColumns(items, []string{"type", "location", "nebula_host"})

	if got := items[0]["type"]; got != "temp-sensor" {
		t.Errorf("type = %v", got)
	}
	if got := items[0]["location"]; got != "hq" {
		t.Errorf("location = %v", got)
	}
	// An unset relation and one the caller cannot read are the same empty cell
	// on purpose: the alternative is printing an id in the column that exists
	// precisely so nobody has to read one.
	if got := items[0]["nebula_host"]; got != "" {
		t.Errorf("nebula_host = %v, want empty", got)
	}
	if _, ok := items[0]["expand"]; ok {
		t.Error("the expand key should be consumed, not printed")
	}
}

func TestResolveRelationColumnsJoinsMultiRelations(t *testing.T) {
	items := []pb.Record{{
		"operations": []any{"a", "b"},
		"expand": map[string]any{
			"operations": []any{
				map[string]any{"collectionName": "thing_type_operations", "name": "heartbeat"},
				map[string]any{"collectionName": "thing_type_operations", "name": "reading"},
			},
		},
	}}

	resolveRelationColumns(items, []string{"operations"})

	if got := items[0]["operations"]; got != "heartbeat,reading" {
		t.Errorf("operations = %v", got)
	}
}

// The one that stops `-o json` quietly acquiring an expand key, and the one that
// stops a relation column being requested for a collection that has none.
func TestRelationColumnsPicksOnlyRelations(t *testing.T) {
	spec, ok := specByCollection("things")
	if !ok {
		t.Fatal("things has no spec")
	}

	got := relationColumns(spec, []string{"id", "code", "name", "type", "location", "active"})
	want := []string{"type", "location"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}

	// organization is injected rather than declared, so it is not in Fields --
	// but `get` prints it, and it is still a relation.
	if got := relationColumns(spec, []string{"organization"}); strings.Join(got, ",") != "organization" {
		t.Errorf("organization should expand on an org-scoped spec, got %v", got)
	}
	global, _ := specByCollection("organizations")
	if got := relationColumns(global, []string{"organization"}); len(got) != 0 {
		t.Errorf("organizations is not org-scoped; got %v", got)
	}
}

// PocketBase applies `fields` to the whole response body, and `expand` is a
// top-level key like any other -- so a projection that names a relation without
// naming `expand` gets the id back and silently loses everything the expand was
// for. Verified against a running server.
func TestWithExpandFieldAddsExpandToAProjection(t *testing.T) {
	if got := withExpandField("code,type"); got != "code,type,expand" {
		t.Errorf("got %q", got)
	}
	// No projection means the whole record, which already carries expand.
	if got := withExpandField(""); got != "" {
		t.Errorf("an empty projection should stay empty, got %q", got)
	}
	if got := withExpandField("code,expand"); got != "code,expand" {
		t.Errorf("expand should not be added twice, got %q", got)
	}
}
