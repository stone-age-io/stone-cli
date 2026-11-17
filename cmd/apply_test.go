package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/stone-age-io/stone-cli/internal/pb"
)

// fakeUpdated is the `updated` the fake answers every write with.
const fakeUpdated = "2026-11-16 12:00:00.000Z"

// fakePB serves one collection's list and records every batch op it is sent.
// Creates get ids "new000000000001", "new000000000002", ...
type fakePB struct {
	records []pb.Record
	sent    []pb.BatchOp
	// When batchStatus is set, /api/batch answers with it and batchBody
	// instead of applying anything.
	batchStatus int
	batchBody   string
	singles     int // creates sent one at a time
}

func (f *fakePB) client(t *testing.T) *pb.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/records"):
			_ = json.NewEncoder(w).Encode(pb.ListResult{Page: 1, TotalPages: 1, Items: f.records})
		case r.Method == http.MethodPost && r.URL.Path == "/api/batch" && f.batchStatus != 0:
			w.WriteHeader(f.batchStatus)
			_, _ = w.Write([]byte(f.batchBody))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/records"):
			f.singles++
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "one00000000000" + string(rune('0'+f.singles)), "updated": fakeUpdated})
		case r.Method == http.MethodPost && r.URL.Path == "/api/batch":
			var body struct {
				Requests []pb.BatchOp `json:"requests"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode batch: %v", err)
			}
			var out []map[string]any
			for _, op := range body.Requests {
				f.sent = append(f.sent, op)
				id := "new" + strings.Repeat("0", 11) + string(rune('0'+len(f.sent)))
				if op.Method == "PATCH" {
					id = path.Base(op.URL)
				}
				out = append(out, map[string]any{"status": 200, "body": map[string]any{"id": id, "updated": fakeUpdated}})
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return pb.New(ctx.Context{URL: srv.URL, Auth: ctx.AuthSection{Token: "not-a-jwt"}})
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func loadApplyFile(t *testing.T, path string) applyFile {
	t.Helper()
	rec, err := pb.UnmarshalFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := rec["updated"].(string)
	pb.Strip(rec)
	id, _ := rec["id"].(string)
	return applyFile{Path: path, Collection: filepath.Base(filepath.Dir(path)), Record: rec, ID: id, Updated: updated}
}

// A file whose `updated` is not the server's has fallen behind the record:
// something changed it after the pull. If the file has something to send, it
// is reported as stale. If it has nothing to send, or carries no `updated`, it
// is not.
func TestApplyReportsStaleFiles(t *testing.T) {
	const pulled, later = "2026-11-01 09:00:00.000Z", "2026-11-02 09:00:00.000Z"
	fake := &fakePB{records: []pb.Record{
		{"id": "aaaaaaaaaaaaaaa", "code": "T-1", "name": "one", "updated": later},
		{"id": "bbbbbbbbbbbbbbb", "code": "T-2", "name": "two", "updated": later},
		{"id": "ccccccccccccccc", "code": "T-3", "name": "three", "updated": later},
		{"id": "ddddddddddddddd", "code": "T-4", "name": "four", "updated": pulled},
	}}
	client := fake.client(t)
	dir := filepath.Join(t.TempDir(), "things")
	writeFile(t, filepath.Join(dir, "T-1.yaml"), "id: aaaaaaaaaaaaaaa\nname: edited\nupdated: "+pulled+"\n") // stale
	writeFile(t, filepath.Join(dir, "T-2.yaml"), "id: bbbbbbbbbbbbbbb\nname: two\nupdated: "+pulled+"\n")    // behind, but sends nothing
	writeFile(t, filepath.Join(dir, "T-3.yaml"), "id: ccccccccccccccc\nname: edited\n")                      // no updated: unchecked
	writeFile(t, filepath.Join(dir, "T-4.yaml"), "id: ddddddddddddddd\nname: edited\nupdated: "+pulled+"\n") // current
	var files []applyFile
	for _, n := range []string{"T-1", "T-2", "T-3", "T-4"} {
		files = append(files, loadApplyFile(t, filepath.Join(dir, n+".yaml")))
	}
	changed, stale, err := diffUpdates(client, "things", files, []int{0, 1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(changed, []int{0, 2, 3}) {
		t.Fatalf("changed = %v", changed)
	}
	if len(stale) != 1 || !strings.Contains(stale[0], "T-1.yaml") {
		t.Fatalf("stale = %q, want only T-1.yaml", stale)
	}
	if _, ok := files[0].Patch["updated"]; ok {
		t.Fatal("updated must never be sent")
	}
}

// An update sends only the keys that differ from the server, and a record that
// matches is not sent at all -- including a JSON file, whose numbers decode as
// float64 where the YAML side has int.
func TestApplyUpdatesSendOnlyWhatChanged(t *testing.T) {
	fake := &fakePB{records: []pb.Record{
		{"id": "aaaaaaaaaaaaaaa", "code": "T-1", "name": "one", "mtu": 1300, "active": false, "collectionName": "things"},
		{"id": "bbbbbbbbbbbbbbb", "code": "T-2", "name": "two", "mtu": 1300},
	}}
	client := fake.client(t)
	dir := filepath.Join(t.TempDir(), "things")
	writeFile(t, filepath.Join(dir, "T-1.yaml"), "id: aaaaaaaaaaaaaaa\ncode: T-1\nname: renamed\nmtu: 1300\n")
	writeFile(t, filepath.Join(dir, "T-2.json"), `{"id": "bbbbbbbbbbbbbbb", "code": "T-2", "name": "two", "mtu": 1300}`)
	files := []applyFile{
		loadApplyFile(t, filepath.Join(dir, "T-1.yaml")),
		loadApplyFile(t, filepath.Join(dir, "T-2.json")),
	}

	changed, _, err := diffUpdates(client, "things", files, []int{0, 1})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(changed, []int{0}) {
		t.Fatalf("changed = %v, want only the renamed record", changed)
	}
	if want := (pb.Record{"name": "renamed"}); !reflect.DeepEqual(files[0].Patch, want) {
		t.Fatalf("patch = %v, want %v -- active must not be sent, the file never mentions it", files[0].Patch, want)
	}

	ok, failed := applyUpdates(client, "things", files, changed)
	if ok != 1 || failed != 0 || len(fake.sent) != 1 {
		t.Fatalf("ok=%d failed=%d sent=%d", ok, failed, len(fake.sent))
	}
	if op := fake.sent[0]; op.Method != "PATCH" || !strings.HasSuffix(op.URL, "/aaaaaaaaaaaaaaa") || !reflect.DeepEqual(op.Body, map[string]any{"name": "renamed"}) {
		t.Fatalf("sent %+v", op)
	}
}

// A record the server does not list (deleted since the pull) keeps its whole
// body, so the server, not apply, decides what happens to it.
func TestApplyUpdateOfAMissingRecordSendsEverything(t *testing.T) {
	fake := &fakePB{}
	client := fake.client(t)
	path := filepath.Join(t.TempDir(), "things", "T-9.yaml")
	writeFile(t, path, "id: zzzzzzzzzzzzzzz\ncode: T-9\n")
	files := []applyFile{loadApplyFile(t, path)}
	changed, _, err := diffUpdates(client, "things", files, []int{0})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || !reflect.DeepEqual(files[0].Patch, pb.Record{"code": "T-9"}) {
		t.Fatalf("changed=%v patch=%v", changed, files[0].Patch)
	}
}

// The id the server assigns is written back, so the next apply updates rather
// than creating a duplicate. If it cannot be written back, that is a failure.
func TestApplyCreateWritesTheIDBack(t *testing.T) {
	fake := &fakePB{}
	client := fake.client(t)
	root := t.TempDir()
	good := filepath.Join(root, "things", "T-1.yaml")
	writeFile(t, good, "code: T-1\n")
	// A file standing where a directory must be makes the write-back fail.
	blocker := filepath.Join(root, "blocker")
	writeFile(t, blocker, "")
	files := []applyFile{
		loadApplyFile(t, good),
		{Path: filepath.Join(blocker, "things", "T-2.yaml"), Collection: "things", Record: pb.Record{"code": "T-2"}},
	}

	ok, failed := applyCreates(client, "things", files, []int{0, 1})
	if ok != 1 || failed != 1 {
		t.Fatalf("ok=%d failed=%d, want the unwritable file counted as failed", ok, failed)
	}
	back, err := pb.UnmarshalFile(good)
	if err != nil {
		t.Fatal(err)
	}
	if back["id"] != "new000000000001" {
		t.Fatalf("id written back = %v", back["id"])
	}
}

func TestApplyRefusesRecordsOfAnotherOrganization(t *testing.T) {
	specs := map[string]EntitySpec{}
	for _, s := range entitySpecs {
		specs[s.Collection] = s
	}
	files := []applyFile{
		{Path: "things/a.yaml", Collection: "things", Record: pb.Record{"organization": "orgA00000000000"}},
		{Path: "memberships/m.yaml", Collection: "memberships", Record: pb.Record{"organization": "orgB00000000000"}},
		{Path: "things/new.yaml", Collection: "things", Record: pb.Record{}},
	}
	if err := checkOrganizations(files[:1], specs, "orgA00000000000"); err != nil {
		t.Fatalf("same org refused: %v", err)
	}
	if err := checkOrganizations(files[2:], specs, "orgA00000000000"); err != nil {
		t.Fatalf("a file with no organization (a create) refused: %v", err)
	}
	err := checkOrganizations(files, specs, "orgA00000000000")
	if err == nil || !strings.Contains(err.Error(), "memberships/m.yaml") || strings.Contains(err.Error(), "things/a.yaml") {
		t.Fatalf("err = %v, want only the other org's membership named", err)
	}
}

// When PocketBase refuses a batch it rolled it back, so the creates are re-sent
// one at a time. When the outcome is unknown -- here a proxy's 502 page -- they
// are not re-sent, because the batch may have committed and a second POST
// would create every record twice.
func TestApplyCreatesFallBackOnlyWhenPocketBaseRefused(t *testing.T) {
	for _, tc := range []struct {
		name, body  string
		status      int
		wantSingles int
		wantOK      int
	}{
		{"batch turned off", `{"status":403,"message":"Batch requests are not allowed."}`, 403, 2, 2},
		{"proxy error", `<html>Bad Gateway</html>`, 502, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakePB{batchStatus: tc.status, batchBody: tc.body}
			client := fake.client(t)
			dir := filepath.Join(t.TempDir(), "things")
			writeFile(t, filepath.Join(dir, "a.yaml"), "code: A\n")
			writeFile(t, filepath.Join(dir, "b.yaml"), "code: B\n")
			files := []applyFile{
				loadApplyFile(t, filepath.Join(dir, "a.yaml")),
				loadApplyFile(t, filepath.Join(dir, "b.yaml")),
			}
			ok, failed := applyCreates(client, "things", files, []int{0, 1})
			if fake.singles != tc.wantSingles || ok != tc.wantOK || ok+failed != 2 {
				t.Fatalf("singles=%d ok=%d failed=%d, want singles=%d ok=%d",
					fake.singles, ok, failed, tc.wantSingles, tc.wantOK)
			}
		})
	}
}
