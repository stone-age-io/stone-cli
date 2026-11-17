package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/adrg/xdg"
	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/stone-age-io/stone-cli/internal/pb"
)

const orgA = "orgA00000000000"

// applyWorkspace runs `stone apply` itself -- context resolution, the
// workspace walk, the organization check, the diff and the write-back -- with
// a throwaway config dir and a fake PocketBase. The other apply tests call the
// pieces; this one checks they are wired together.
func applyWorkspace(t *testing.T, flags map[string]string) (fake *fakePB, ws string, run func() error) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	xdg.Reload()
	t.Cleanup(xdg.Reload)

	fake = &fakePB{records: []pb.Record{
		{"id": "aaaaaaaaaaaaaaa", "organization": orgA, "code": "T-1", "name": "one"},
		{"id": "bbbbbbbbbbbbbbb", "organization": orgA, "code": "T-2", "name": "two"},
	}}
	client := fake.client(t)

	ws = t.TempDir()
	writeFile(t, filepath.Join(ws, "things", "T-1.yaml"), "id: aaaaaaaaaaaaaaa\norganization: "+orgA+"\ncode: T-1\nname: renamed\n")
	writeFile(t, filepath.Join(ws, "things", "T-2.yaml"), "id: bbbbbbbbbbbbbbb\norganization: "+orgA+"\ncode: T-2\nname: two\n")
	writeFile(t, filepath.Join(ws, "things", "T-3.yaml"), "code: T-3\n")
	writeFile(t, filepath.Join(ws, "thing", "typo.yaml"), "code: T-4\n")

	if err := ctx.Save(ctx.Context{
		Name:                "e2e",
		URL:                 client.BaseURL,
		Auth:                ctx.AuthSection{Collection: "users", Token: "not-a-jwt"},
		CurrentOrganization: orgA,
		Workspace:           ws,
	}); err != nil {
		t.Fatal(err)
	}
	prev := flagContext
	flagContext = "e2e"
	t.Cleanup(func() { flagContext = prev })
	for k, v := range flags {
		if err := applyCmd.Flags().Set(k, v); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = applyCmd.Flags().Set(k, "false") })
	}
	return fake, ws, func() error { return runApply(applyCmd, nil) }
}

func TestApplyCommand(t *testing.T) {
	fake, ws, run := applyWorkspace(t, nil)
	if err := run(); err != nil {
		t.Fatal(err)
	}
	// One create, with the organization injected; one PATCH carrying only the
	// changed field; nothing for the unchanged record or the typo'd directory.
	want := []pb.BatchOp{
		{Method: "POST", URL: "/api/collections/things/records", Body: map[string]any{"code": "T-3", "organization": orgA}},
		{Method: "PATCH", URL: "/api/collections/things/records/aaaaaaaaaaaaaaa", Body: map[string]any{"name": "renamed"}},
	}
	if !reflect.DeepEqual(fake.sent, want) {
		t.Fatalf("sent\n  %+v\nwant\n  %+v", fake.sent, want)
	}
	back, err := pb.UnmarshalFile(filepath.Join(ws, "things", "T-3.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if back["id"] != "new000000000001" || back["updated"] != fakeUpdated {
		t.Fatalf("T-3.yaml id = %v updated = %v, want both written back", back["id"], back["updated"])
	}

	// Run again: the created record now has an id, so the second apply must
	// not create it a second time.
	fake.sent = nil
	fake.records = append(fake.records, pb.Record{"id": "new000000000001", "organization": orgA, "code": "T-3"})
	fake.records[0]["name"] = "renamed"
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 0 {
		t.Fatalf("second apply sent %+v, want nothing", fake.sent)
	}
}

func TestApplyCommandDryRunSendsNothing(t *testing.T) {
	fake, ws, run := applyWorkspace(t, map[string]string{"dry-run": "true"})
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 0 {
		t.Fatalf("dry run sent %+v", fake.sent)
	}
	data, err := os.ReadFile(filepath.Join(ws, "things", "T-3.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "code: T-3\n" {
		t.Fatalf("dry run rewrote T-3.yaml:\n%s", data)
	}
}

func TestApplyCommandStopsOnAnotherOrganization(t *testing.T) {
	fake, ws, run := applyWorkspace(t, nil)
	writeFile(t, filepath.Join(ws, "things", "B-1.yaml"), "id: ccccccccccccccc\norganization: orgB00000000000\ncode: B-1\n")
	err := run()
	if err == nil || !strings.Contains(err.Error(), "B-1.yaml") {
		t.Fatalf("err = %v, want B-1.yaml named", err)
	}
	if len(fake.sent) != 0 {
		t.Fatalf("sent %+v before refusing", fake.sent)
	}
}

// A file older than its record is refused before anything is sent -- the
// create beside it included -- and --force sends it anyway. Either way the
// record's new `updated` lands in the file, so the next apply is not refused
// for the change apply itself just made.
func TestApplyCommandRefusesStaleFiles(t *testing.T) {
	const pulled, later = "2026-11-01 09:00:00.000Z", "2026-11-02 09:00:00.000Z"
	fake, ws, run := applyWorkspace(t, nil)
	fake.records[0]["updated"] = later
	t1 := filepath.Join(ws, "things", "T-1.yaml")
	writeFile(t, t1, "id: aaaaaaaaaaaaaaa\norganization: "+orgA+"\ncode: T-1\nname: renamed\nupdated: "+pulled+"\n")

	err := run()
	if err == nil || !strings.Contains(err.Error(), "T-1.yaml") || !strings.Contains(err.Error(), later) {
		t.Fatalf("err = %v, want T-1.yaml named with the server's time", err)
	}
	if len(fake.sent) != 0 {
		t.Fatalf("sent %+v before refusing", fake.sent)
	}

	if err := applyCmd.Flags().Set("force", "true"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = applyCmd.Flags().Set("force", "false") })
	if err := run(); err != nil {
		t.Fatal(err)
	}
	var patched bool
	for _, op := range fake.sent {
		patched = patched || (op.Method == "PATCH" && strings.HasSuffix(op.URL, "/aaaaaaaaaaaaaaa"))
		if _, ok := op.Body["updated"]; ok {
			t.Fatalf("sent updated in %+v", op)
		}
	}
	if !patched {
		t.Fatalf("--force did not send T-1: %+v", fake.sent)
	}
	back, err := pb.UnmarshalFile(t1)
	if err != nil {
		t.Fatal(err)
	}
	if back["updated"] != fakeUpdated {
		t.Fatalf("T-1.yaml updated = %v, want the server's new %s", back["updated"], fakeUpdated)
	}
}

// pull keeps `updated` -- the one server-only field a workspace needs -- and
// still drops the rest.
func TestPullKeepsUpdated(t *testing.T) {
	fake, ws, _ := applyWorkspace(t, nil)
	c, err := ctx.Load("e2e")
	if err != nil {
		t.Fatal(err)
	}
	c.Auth.Collection = "_superusers" // an operator: pull asks nothing about the caller
	if err := ctx.Save(c); err != nil {
		t.Fatal(err)
	}
	fake.records[0]["updated"] = "2026-11-02 09:00:00.000Z"
	fake.records[0]["created"] = "2026-11-01 09:00:00.000Z"
	fake.records[0]["collectionId"] = "pbc_123"
	if err := runPull(pullCmd, []string{"things"}); err != nil {
		t.Fatal(err)
	}
	got, err := pb.UnmarshalFile(filepath.Join(ws, "things", "T-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got["updated"] != "2026-11-02 09:00:00.000Z" {
		t.Fatalf("updated = %v", got["updated"])
	}
	for _, k := range []string{"created", "collectionId"} {
		if _, ok := got[k]; ok {
			t.Fatalf("pull wrote %s", k)
		}
	}
}
