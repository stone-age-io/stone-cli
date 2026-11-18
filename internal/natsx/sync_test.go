package natsx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContextOrgID(t *testing.T) {
	dir := t.TempDir()
	write := func(name, desc string) string {
		p := filepath.Join(dir, name+".json")
		if err := os.WriteFile(p, []byte(`{"description":"`+desc+`","url":"nats://x"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	stone := defaultDescription(SyncOptions{StoneContext: "prod", OrgName: "acme", OrgID: "org123"})
	cases := map[string]string{
		write("stone", stone): "org123",
		// Written before org-id was recorded, or by hand: nothing to check.
		write("old", managedPrefix+"; stone-context=prod; org=acme"): "",
		write("mine", "org-id=org999"):                               "",
		filepath.Join(dir, "missing.json"):                           "",
	}
	for path, want := range cases {
		if got := ContextOrgID(path); got != want {
			t.Errorf("%s: got %q, want %q", filepath.Base(path), got, want)
		}
	}
}
