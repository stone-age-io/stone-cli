package cmd

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/spf13/cobra"
)

// withConfigFlag sets --config on cmd for one test.
func withConfigFlag(t *testing.T, cmd *cobra.Command, path string) {
	t.Helper()
	if err := cmd.Flags().Set("config", path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Flags().Set("config", "")
		cmd.Flags().Lookup("config").Changed = false
	})
}

// A --config file uses the keys the server and `nats` use (max_age, not
// MaxAge), and a file `stone js pull` wrote must work as one.
func TestStreamConfigFile(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ name, body string }{
		{"stream.yaml", "name: ignored\nsubjects: [orders.>]\nmax_age: 24h\nstorage: memory\nretention: workqueue\nnum_replicas: 3\n" +
			"consumer_limits:\n  inactive_threshold: 90s\norganization: orgA00000000000\nconfig_hash: abc\n"},
		{"stream.json", `{"subjects":["orders.>"],"max_age":86400000000000,"storage":"memory","retention":"workqueue","num_replicas":3,"consumer_limits":{"inactive_threshold":"90s"}}`},
	} {
		path := filepath.Join(dir, tc.name)
		writeFile(t, path, tc.body)
		withConfigFlag(t, jsStreamCreateCmd, path)
		cfg, err := streamConfigFromFlags(jsStreamCreateCmd, "ORDERS")
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if cfg.Name != "ORDERS" || cfg.MaxAge != 24*time.Hour || cfg.Storage != jetstream.MemoryStorage ||
			cfg.Retention != jetstream.WorkQueuePolicy || cfg.Replicas != 3 ||
			cfg.ConsumerLimits.InactiveThreshold != 90*time.Second || len(cfg.Subjects) != 1 {
			t.Fatalf("%s: decoded %+v", tc.name, cfg)
		}
	}

	path := filepath.Join(dir, "typo.yaml")
	writeFile(t, path, "subjects: [orders.>]\nmax_ages: 24h\n")
	withConfigFlag(t, jsStreamCreateCmd, path)
	if _, err := streamConfigFromFlags(jsStreamCreateCmd, "ORDERS"); err == nil || !strings.Contains(err.Error(), "max_ages") {
		t.Fatalf("a misspelt key was accepted (err = %v)", err)
	}
}

func TestBucketConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twin.yaml")
	writeFile(t, path, "bucket: ignored\nhistory: 10\nttl: 720h\nstorage: memory\ncompression: true\nconfig_hash: abc\n")
	withConfigFlag(t, jsBucketCreateCmd, path)
	cfg, err := kvConfigFromFlags(jsBucketCreateCmd, "twin")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Bucket != "twin" || cfg.History != 10 || cfg.TTL != 720*time.Hour || cfg.Storage != jetstream.MemoryStorage || !cfg.Compression {
		t.Fatalf("decoded %+v", cfg)
	}
}
