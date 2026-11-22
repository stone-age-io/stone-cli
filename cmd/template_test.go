package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/stone-age-io/stone-cli/internal/pb"
)

// templatePB is a PocketBase holding several collections, which a template
// needs: it creates operations and then a thing type naming them.
type templatePB struct {
	t     *testing.T
	colls map[string][]pb.Record
	sent  []string // "POST coll {body}" / "PATCH coll/id {body}"
	next  int
}

func (f *templatePB) serve() ctx.Context {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/collections/"), "/")
		coll := parts[0]
		switch {
		case r.Method == http.MethodGet && len(parts) == 2:
			_ = json.NewEncoder(w).Encode(pb.ListResult{Page: 1, TotalPages: 1, Items: f.colls[coll]})
		case r.Method == http.MethodPost && len(parts) == 2:
			var body pb.Record
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.sent = append(f.sent, "POST "+coll+" "+canon(body))
			f.next++
			body["id"] = fmt.Sprintf("new%012d", f.next)
			f.colls[coll] = append(f.colls[coll], body)
			_ = json.NewEncoder(w).Encode(body)
		case r.Method == http.MethodPatch && len(parts) == 3:
			var body pb.Record
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.sent = append(f.sent, "PATCH "+coll+"/"+parts[2]+" "+canon(body))
			for _, rec := range f.colls[coll] {
				if rec["id"] == parts[2] {
					for k, v := range body {
						rec[k] = v
					}
					_ = json.NewEncoder(w).Encode(rec)
					return
				}
			}
			http.NotFound(w, r)
		default:
			f.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	f.t.Cleanup(srv.Close)
	return ctx.Context{URL: srv.URL, Auth: ctx.AuthSection{Collection: "users", Token: "not-a-jwt"}, CurrentOrganization: orgA}
}

func canon(r pb.Record) string {
	b, _ := json.Marshal(r) // map keys marshal sorted
	return string(b)
}

// writeContract writes a small application contract: two operations and the
// thing type naming them, by name.
func writeContract(t *testing.T) string {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "thing_type_operations", "publish_door_decision.yaml"),
		"name: publish_door_decision\ncapability: publish\nsubject_suffix: evt.door.{thing}.tap\n")
	writeFile(t, filepath.Join(dir, "thing_type_operations", "subscribe_door_grant.yaml"),
		"name: subscribe_door_grant\ncapability: subscribe\nsubject_suffix: cmd.door.{thing}.grant\n")
	writeFile(t, filepath.Join(dir, "thing_types", "door.yaml"),
		"code: door\nname: Door\nsubject_prefix: acc.{location}\noperations:\n  - publish_door_decision\n  - subscribe_door_grant\n")
	return dir
}

func readTree(t *testing.T, dir string) map[string]string {
	out := map[string]string{}
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(p)
			out[p] = string(b)
		}
		return nil
	})
	return out
}

// A fresh organization: the operations are created first, the thing type
// names them by the ids they got, every create carries the current
// organization, and the template's files are not touched. A second apply sends
// nothing.
func TestTemplateOnboardsAnOrganization(t *testing.T) {
	fake := &templatePB{t: t, colls: map[string][]pb.Record{
		// Another organization's operation with the same name must not match.
		"thing_type_operations": {{"id": "other0000000001", "organization": "orgB00000000000", "name": "publish_door_decision", "capability": "publish"}},
	}}
	c := fake.serve()
	dir := writeContract(t)
	before := readTree(t, dir)

	if err := runApplyTemplate(c, []string{dir}, false, false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`POST thing_type_operations {"capability":"publish","name":"publish_door_decision","organization":"` + orgA + `","subject_suffix":"evt.door.{thing}.tap"}`,
		`POST thing_type_operations {"capability":"subscribe","name":"subscribe_door_grant","organization":"` + orgA + `","subject_suffix":"cmd.door.{thing}.grant"}`,
		`POST thing_types {"code":"door","name":"Door","operations":["new000000000001","new000000000002"],"organization":"` + orgA + `","subject_prefix":"acc.{location}"}`,
	}
	if !reflect.DeepEqual(fake.sent, want) {
		t.Fatalf("sent\n  %s\nwant\n  %s", strings.Join(fake.sent, "\n  "), strings.Join(want, "\n  "))
	}
	if after := readTree(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatal("apply --template wrote to the template; the next organization would get this one's ids")
	}

	fake.sent = nil
	if err := runApplyTemplate(c, []string{dir}, false, false); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 0 {
		t.Fatalf("second apply sent %v, want nothing", fake.sent)
	}
}

// An organization on an older contract gets only the differences. A code
// matches whatever its case, as it does everywhere else, and keeps its case.
func TestTemplateUpdatesOnlyWhatDiffers(t *testing.T) {
	fake := &templatePB{t: t, colls: map[string][]pb.Record{
		"thing_type_operations": {
			{"id": "op0000000000001", "organization": orgA, "name": "publish_door_decision", "capability": "publish", "subject_suffix": "event.door.{thing}.tap"},
			{"id": "op0000000000002", "organization": orgA, "name": "subscribe_door_grant", "capability": "subscribe", "subject_suffix": "cmd.door.{thing}.grant"},
		},
		"thing_types": {
			{"id": "tt0000000000001", "organization": orgA, "code": "DOOR", "name": "Door", "subject_prefix": "acc.{location}", "operations": []any{"op0000000000001"}},
		},
	}}
	c := fake.serve()
	if err := runApplyTemplate(c, []string{writeContract(t)}, false, false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`PATCH thing_type_operations/op0000000000001 {"subject_suffix":"evt.door.{thing}.tap"}`,
		`PATCH thing_types/tt0000000000001 {"operations":["op0000000000001","op0000000000002"]}`,
	}
	if !reflect.DeepEqual(fake.sent, want) {
		t.Fatalf("sent\n  %s\nwant\n  %s", strings.Join(fake.sent, "\n  "), strings.Join(want, "\n  "))
	}
}

// A dry run against a fresh organization resolves a relation to an operation
// the template would create, and sends nothing.
func TestTemplateDryRunSendsNothing(t *testing.T) {
	fake := &templatePB{t: t, colls: map[string][]pb.Record{}}
	c := fake.serve()
	err := runApplyTemplate(c, []string{writeContract(t)}, true, true)
	if err != errChangesPending {
		t.Fatalf("err = %v, want errChangesPending", err)
	}
	if len(fake.sent) != 0 {
		t.Fatalf("dry run sent %v", fake.sent)
	}
}

// What a template refuses, before anything is sent.
func TestTemplateRefuses(t *testing.T) {
	cases := []struct {
		name, path, body, want string
	}{
		{"an id", "thing_types/door.yaml", "id: abc\ncode: door\n", `no "id"`},
		{"an organization", "thing_types/door.yaml", "organization: x\ncode: door\n", `no "organization"`},
		{"no key", "thing_types/door.yaml", "name: Door\n", "no code"},
		{"a collection not per organization", "memberships/m.yaml", "role: admin\n", "not a collection a template can hold"},
		{"an instance, not a definition", "things/t.yaml", "code: T-1\n", "not a collection a template can hold"},
		{"an unknown relation", "thing_types/door.yaml", "code: door\noperations: [nope]\n", `no thing-type-operation named "nope"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &templatePB{t: t, colls: map[string][]pb.Record{}}
			c := fake.serve()
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, filepath.FromSlash(tc.path)), tc.body)
			err := runApplyTemplate(c, []string{dir}, false, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
			if len(fake.sent) != 0 {
				t.Fatalf("sent %v", fake.sent)
			}
		})
	}
}

// Two files declaring one record is a mistake in the template.
func TestTemplateRefusesADuplicateKey(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "thing_types", "a.yaml"), "code: door\n")
	writeFile(t, filepath.Join(dir, "thing_types", "b.yaml"), "code: DOOR\n")
	if _, err := readTemplate([]string{dir}); err == nil || !strings.Contains(err.Error(), "both declare") {
		t.Fatalf("err = %v, want a duplicate", err)
	}
}

// Operations go before the thing types that name them, whatever entitySpecs
// says, and a collection nothing points at keeps entitySpecs order.
func TestTemplateOrder(t *testing.T) {
	files := []templateFile{{Collection: "thing_types"}, {Collection: "nats_roles"}, {Collection: "thing_type_operations"}}
	got, err := templateOrder(files)
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, c := range got {
		pos[c] = i
	}
	if len(got) != 3 || pos["thing_type_operations"] > pos["thing_types"] {
		t.Fatalf("order = %v, want operations before thing types", got)
	}
}

// Every template collection must be one the template code can work with:
// one record set per organization, writable, and found by a key.
func TestTemplateCollectionsAreKeyed(t *testing.T) {
	for coll := range templateCollections {
		spec, ok := specByCollection(coll)
		if !ok || !spec.OrgScoped || !spec.syncable() || spec.LookupKey == "" {
			t.Errorf("%s: want an org-scoped, writable spec with a LookupKey", coll)
		}
	}
}
