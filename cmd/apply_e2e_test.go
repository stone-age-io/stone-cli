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
	if back["id"] != "new000000000001" {
		t.Fatalf("T-3.yaml id = %v, want the created id written back", back["id"])
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
