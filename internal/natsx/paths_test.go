package natsx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adrg/xdg"
	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/stone-age-io/stone-cli/internal/pb"
	"github.com/synadia-io/orbit.go/natscontext"
)

const fakeCreds = `-----BEGIN NATS USER JWT-----
eyJhbGciOiJlZDI1NTE5LW5rZXkifQ.e30.fake
------END NATS USER JWT------

-----BEGIN USER NKEY SEED-----
SUAGY3JQSDVYX7YJDPQ2FVGFGMS2CQJH2TCEPGGPTQBGB3KMBQPFEXAMPLE
------END USER NKEY SEED------
`

// The nats tooling resolves its config dir as $XDG_CONFIG_HOME, else
// $HOME/.config — on every OS. Anything that resolves differently (notably
// xdg.ConfigHome, which is %LOCALAPPDATA% on Windows and
// ~/Library/Application Support on macOS) writes contexts nats-cli and
// natscontext.Connect cannot see.
func TestContextDirFollowsXDGConfigHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	if got, want := ContextDir(), filepath.Join(dir, "nats", "context"); got != want {
		t.Errorf("ContextDir() = %s, want %s", got, want)
	}
	if got, want := SelectedContextPath(), filepath.Join(dir, "nats", "context.txt"); got != want {
		t.Errorf("SelectedContextPath() = %s, want %s", got, want)
	}
}

func TestContextDirFallsBackToHomeDotConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	if got, want := ContextDir(), filepath.Join(home, ".config", "nats", "context"); got != want {
		t.Errorf("ContextDir() = %s, want %s", got, want)
	}
}

// End-to-end guard on the same contract: whatever SyncContextForOrg writes,
// natscontext must be able to load by name. A failure here means sync-context
// reports success and every later `stone nats ...` call dies with
// `unknown context`.
func TestSyncedContextIsVisibleToNatscontext(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	xdg.Reload()
	t.Cleanup(xdg.Reload)

	res, err := SyncContextForOrg(SyncOptions{
		StoneContext: "unit",
		OrgName:      "Acme Corp",
		OrgID:        "abc123def456ghi",
		// Port 1 refuses immediately: we only care about context lookup.
		NATSURL:  "nats://127.0.0.1:1",
		NATSUser: pb.Record{"creds_file": fakeCreds},
	})
	if err != nil {
		t.Fatalf("SyncContextForOrg: %v", err)
	}
	if res.Name != "stone-unit-acme-corp" {
		t.Errorf("context name = %q, want stone-unit-acme-corp", res.Name)
	}
	if _, err := os.Stat(res.CtxPath); err != nil {
		t.Fatalf("context file: %v", err)
	}
	if res.CtxPath != ContextPath(res.Name) {
		t.Errorf("wrote %s, want %s", res.CtxPath, ContextPath(res.Name))
	}

	_, _, err = natscontext.Connect(res.Name)
	if err == nil {
		t.Fatal("expected a dial failure against 127.0.0.1:1")
	}
	if strings.Contains(err.Error(), "unknown context") {
		t.Fatalf("natscontext cannot see the context stone just wrote: %v", err)
	}
}

// An unset or missing context must fail loudly. Falling through to the user's
// selected nats-cli context connects to an unrelated server and looks like a
// subscription that never receives anything.
func TestConnectRefusesUnsetContext(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	_, _, err := Connect(ctx.Context{Name: "unit", NATSURL: "nats://127.0.0.1:1"})
	if err == nil {
		t.Fatal("connected with no nats context set")
	}
	if !strings.Contains(err.Error(), "sync-context") {
		t.Errorf("error should point at sync-context, got: %v", err)
	}
}

func TestConnectReportsMissingContextFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	_, _, err := Connect(ctx.Context{Name: "unit", NATSContext: "stone-unit-nope"})
	if err == nil {
		t.Fatal("connected with a context that does not exist")
	}
	if !strings.Contains(err.Error(), ContextPath("stone-unit-nope")) {
		t.Errorf("error should name the path searched, got: %v", err)
	}
}
