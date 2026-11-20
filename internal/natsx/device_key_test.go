package natsx

import (
	"strings"
	"testing"

	"github.com/stone-age-io/stone-cli/internal/pb"
)

// A device-held key has no seed on the platform, so there is no credential to
// write, and the old advice (set regenerate) would re-sign a JWT the CLI still
// cannot use. The error has to say what is actually going on.
func TestSyncContextRefusesADeviceHeldKeyByName(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	_, err := SyncContextForOrg(SyncOptions{
		StoneContext: "test", OrgName: "Org", NATSURL: "nats://127.0.0.1:4222",
		NATSUser: pb.Record{"device_key": true, "jwt": "eyJ...", "creds_file": ""},
	})
	if err == nil || !strings.Contains(err.Error(), "held by its device") {
		t.Fatalf("SyncContextForOrg on a device-held key: %v, want an error naming the device-held key", err)
	}
	if strings.Contains(err.Error(), "regenerate") {
		t.Error("the error advises regenerate, which cannot produce a credential for a device-held key")
	}
}
