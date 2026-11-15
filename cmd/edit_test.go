package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stone-age-io/stone-cli/internal/pb"
	"gopkg.in/yaml.v3"
)

// A nebula_hosts record as the API returns it: JSON-decoded, so numbers are
// float64 and arrays are []any. revoked_certificates is what a stale copy must
// not overwrite.
const editedHostJSON = `{
  "id": "abcdefghijklmno",
  "hostname": "edge-1",
  "groups": ["admin"],
  "active": true,
  "mtu": 1300,
  "certificate": "-----BEGIN NEBULA CERTIFICATE V2-----\nC2\n-----END NEBULA CERTIFICATE V2-----\n",
  "revoked_certificates": ["-----BEGIN NEBULA CERTIFICATE V2-----\nC1\n-----END NEBULA CERTIFICATE V2-----\n"],
  "retired_certificates": [],
  "firewall_inbound": [{"port": "any", "proto": "icmp", "host": "any"}]
}`

// openAndSave does what `edit` does around the editor: marshal the record to
// YAML, parse that back as the "opened" copy, let change() play the user, then
// write the result to a .yaml file and read it with pb.UnmarshalFile.
func openAndSave(t *testing.T, change func(pb.Record)) (opened, saved pb.Record) {
	t.Helper()
	cur := pb.Record{}
	if err := json.Unmarshal([]byte(editedHostJSON), &cur); err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(cur)
	if err != nil {
		t.Fatal(err)
	}
	opened = pb.Record{}
	if err := yaml.Unmarshal(data, &opened); err != nil {
		t.Fatal(err)
	}
	inEditor := pb.Record{}
	if err := yaml.Unmarshal(data, &inEditor); err != nil {
		t.Fatal(err)
	}
	change(inEditor)
	path := filepath.Join(t.TempDir(), "host.yaml")
	out, err := yaml.Marshal(inEditor)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	saved, err = pb.UnmarshalFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return opened, saved
}

func patchKeys(p pb.Record) []string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// edit used to PATCH the whole file back, so a re-issue between opening the
// editor and saving lost its revoked_certificates entry, and a deactivation in
// that window was undone by the stale `active: true`. Only what the user
// changed may be sent.
func TestEditSendsOnlyWhatChanged(t *testing.T) {
	cases := []struct {
		name   string
		change func(pb.Record)
		want   []string
	}{
		{
			name:   "nothing changed",
			change: func(pb.Record) {},
			want:   []string{},
		},
		{
			name:   "one field changed",
			change: func(r pb.Record) { r["groups"] = []any{"ops"} },
			want:   []string{"groups"},
		},
		{
			name: "a deleted line is not sent",
			change: func(r pb.Record) {
				delete(r, "revoked_certificates")
				r["hostname"] = "edge-2"
			},
			want: []string{"hostname"},
		},
		{
			name:   "id is never sent",
			change: func(r pb.Record) { r["id"] = "zzzzzzzzzzzzzzz" },
			want:   []string{},
		},
		{
			name:   "a hand-written field is sent",
			change: func(r pb.Record) { r["renew"] = true },
			want:   []string{"renew"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opened, saved := openAndSave(t, tc.change)
			got := patchKeys(editPatch(opened, saved))
			if len(got) != len(tc.want) {
				t.Fatalf("patch keys = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("patch keys = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
