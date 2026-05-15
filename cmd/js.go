package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/spf13/cobra"
	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/stone-age-io/stone-cli/internal/natsx"
	"github.com/stone-age-io/stone-cli/internal/pb"
	"gopkg.in/yaml.v3"
)

var jsCmd = &cobra.Command{
	Use:   "js",
	Short: "Manage JetStream streams and KV buckets",
	Long: `JetStream administration commands. The data-plane KV operations
(get/put/del/watch/ls keys) live under 'stone kv'.`,
}

// ---- streams ----------------------------------------------------------------

var jsStreamCmd = &cobra.Command{
	Use:     "stream",
	Aliases: []string{"streams"},
	Short:   "Manage JetStream streams",
}

var jsStreamLsCmd = &cobra.Command{
	Use:     "ls",
	Aliases: []string{"list"},
	Short:   "List streams",
	RunE: func(cmd *cobra.Command, args []string) error {
		js, cleanup, err := openJS()
		if err != nil {
			return err
		}
		defer cleanup()
		ctx2, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		lister := js.ListStreams(ctx2)
		var rows []pb.Record
		for info := range lister.Info() {
			rows = append(rows, pb.Record{
				"name":     info.Config.Name,
				"subjects": strings.Join(info.Config.Subjects, ","),
				"messages": info.State.Msgs,
				"bytes":    info.State.Bytes,
				"storage":  storageName(info.Config.Storage),
			})
		}
		if err := lister.Err(); err != nil {
			return err
		}
		return pb.PrintList(os.Stdout, rows, []string{"name", "subjects", "messages", "bytes", "storage"}, resolveOutput())
	},
}

var jsStreamInfoCmd = &cobra.Command{
	Use:   "info <name>",
	Short: "Show stream config and state",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		js, cleanup, err := openJS()
		if err != nil {
			return err
		}
		defer cleanup()
		ctx2, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stream, err := js.Stream(ctx2, args[0])
		if err != nil {
			return err
		}
		info, err := stream.Info(ctx2)
		if err != nil {
			return err
		}
		rec, err := structToRecord(info)
		if err != nil {
			return err
		}
		return pb.PrintRecord(os.Stdout, rec, resolveOutput())
	},
}

var jsStreamCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a stream",
	Long: `Create a stream with common flags. Advanced configuration:
  --config <file>     load a YAML or JSON jetstream.StreamConfig (the file's
                      name field is ignored; the positional <name> wins)`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		js, cleanup, err := openJS()
		if err != nil {
			return err
		}
		defer cleanup()

		cfg, err := streamConfigFromFlags(cmd, args[0])
		if err != nil {
			return err
		}
		ctx2, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stream, err := js.CreateStream(ctx2, cfg)
		if err != nil {
			return err
		}
		info, err := stream.Info(ctx2)
		if err != nil {
			return err
		}
		rec, _ := structToRecord(info)
		return pb.PrintRecord(os.Stdout, rec, resolveOutput())
	},
}

var jsStreamPurgeCmd = &cobra.Command{
	Use:   "purge <name>",
	Short: "Purge all messages from a stream",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		yes, _ := cmd.Flags().GetBool("yes")
		if !yes && !confirm("purge ALL messages from stream %q? [y/N] ", args[0]) {
			return errors.New("aborted")
		}
		js, cleanup, err := openJS()
		if err != nil {
			return err
		}
		defer cleanup()
		ctx2, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stream, err := js.Stream(ctx2, args[0])
		if err != nil {
			return err
		}
		if err := stream.Purge(ctx2); err != nil {
			return err
		}
		fmt.Printf("purged %s\n", args[0])
		return nil
	},
}

var jsStreamDeleteCmd = &cobra.Command{
	Use:     "delete <name>",
	Aliases: []string{"rm"},
	Short:   "Delete a stream",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		yes, _ := cmd.Flags().GetBool("yes")
		if !yes && !confirm("delete stream %q? [y/N] ", args[0]) {
			return errors.New("aborted")
		}
		js, cleanup, err := openJS()
		if err != nil {
			return err
		}
		defer cleanup()
		ctx2, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := js.DeleteStream(ctx2, args[0]); err != nil {
			return err
		}
		fmt.Printf("deleted %s\n", args[0])
		return nil
	},
}

// ---- KV buckets -------------------------------------------------------------

var jsBucketCmd = &cobra.Command{
	Use:     "bucket",
	Aliases: []string{"buckets", "kv"},
	Short:   "Manage JetStream KV buckets (lifecycle only; use 'stone kv' for data)",
}

var jsBucketLsCmd = &cobra.Command{
	Use:     "ls",
	Aliases: []string{"list"},
	Short:   "List KV buckets",
	RunE: func(cmd *cobra.Command, args []string) error {
		js, cleanup, err := openJS()
		if err != nil {
			return err
		}
		defer cleanup()
		ctx2, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		lister := js.KeyValueStores(ctx2)
		var rows []pb.Record
		for status := range lister.Status() {
			rows = append(rows, pb.Record{
				"name":    status.Bucket(),
				"values":  status.Values(),
				"bytes":   status.Bytes(),
				"history": status.History(),
				"ttl":     status.TTL().String(),
			})
		}
		if err := lister.Error(); err != nil {
			return err
		}
		return pb.PrintList(os.Stdout, rows, []string{"name", "values", "bytes", "history", "ttl"}, resolveOutput())
	},
}

var jsBucketInfoCmd = &cobra.Command{
	Use:   "info <name>",
	Short: "Show KV bucket status",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		js, cleanup, err := openJS()
		if err != nil {
			return err
		}
		defer cleanup()
		ctx2, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		kv, err := js.KeyValue(ctx2, args[0])
		if err != nil {
			return err
		}
		st, err := kv.Status(ctx2)
		if err != nil {
			return err
		}
		rec := pb.Record{
			"name":         st.Bucket(),
			"values":       st.Values(),
			"bytes":        st.Bytes(),
			"history":      st.History(),
			"ttl":          st.TTL().String(),
			"backing":      st.BackingStore(),
			"is_compressed": st.IsCompressed(),
		}
		return pb.PrintRecord(os.Stdout, rec, resolveOutput())
	},
}

var jsBucketCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a KV bucket",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		js, cleanup, err := openJS()
		if err != nil {
			return err
		}
		defer cleanup()
		cfg, err := kvConfigFromFlags(cmd, args[0])
		if err != nil {
			return err
		}
		ctx2, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		kv, err := js.CreateKeyValue(ctx2, cfg)
		if err != nil {
			return err
		}
		st, err := kv.Status(ctx2)
		if err != nil {
			return err
		}
		fmt.Printf("created bucket %s (history=%d ttl=%s)\n", st.Bucket(), st.History(), st.TTL())
		return nil
	},
}

var jsBucketDeleteCmd = &cobra.Command{
	Use:     "delete <name>",
	Aliases: []string{"rm"},
	Short:   "Delete a KV bucket",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		yes, _ := cmd.Flags().GetBool("yes")
		if !yes && !confirm("delete KV bucket %q (all data lost)? [y/N] ", args[0]) {
			return errors.New("aborted")
		}
		js, cleanup, err := openJS()
		if err != nil {
			return err
		}
		defer cleanup()
		ctx2, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := js.DeleteKeyValue(ctx2, args[0]); err != nil {
			return err
		}
		fmt.Printf("deleted bucket %s\n", args[0])
		return nil
	},
}

// ---- helpers ----------------------------------------------------------------

func openJS() (jetstream.JetStream, func(), error) {
	c, err := ctx.Active(flagContext)
	if err != nil {
		return nil, nil, err
	}
	nc, settings, err := natsx.Connect(c)
	if err != nil {
		return nil, nil, err
	}
	js, err := natsx.JetStream(nc, settings)
	if err != nil {
		nc.Drain()
		return nil, nil, err
	}
	return js, func() { nc.Drain() }, nil
}

func streamConfigFromFlags(cmd *cobra.Command, name string) (jetstream.StreamConfig, error) {
	cfg := jetstream.StreamConfig{Name: name}

	if path, _ := cmd.Flags().GetString("config"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("read --config: %w", err)
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".yaml", ".yml":
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return cfg, fmt.Errorf("parse %s: %w", path, err)
			}
		default:
			if err := json.Unmarshal(data, &cfg); err != nil {
				return cfg, fmt.Errorf("parse %s: %w", path, err)
			}
		}
		cfg.Name = name
	}

	if v, _ := cmd.Flags().GetStringSlice("subject"); len(v) > 0 {
		cfg.Subjects = v
	}
	if cmd.Flags().Changed("max-msgs") {
		v, _ := cmd.Flags().GetInt64("max-msgs")
		cfg.MaxMsgs = v
	}
	if cmd.Flags().Changed("max-bytes") {
		v, _ := cmd.Flags().GetInt64("max-bytes")
		cfg.MaxBytes = v
	}
	if cmd.Flags().Changed("max-age") {
		v, _ := cmd.Flags().GetDuration("max-age")
		cfg.MaxAge = v
	}
	if cmd.Flags().Changed("retention") {
		v, _ := cmd.Flags().GetString("retention")
		r, err := parseRetention(v)
		if err != nil {
			return cfg, err
		}
		cfg.Retention = r
	}
	if cmd.Flags().Changed("storage") {
		v, _ := cmd.Flags().GetString("storage")
		s, err := parseStorage(v)
		if err != nil {
			return cfg, err
		}
		cfg.Storage = s
	}
	if cmd.Flags().Changed("replicas") {
		v, _ := cmd.Flags().GetInt("replicas")
		cfg.Replicas = v
	}
	if len(cfg.Subjects) == 0 {
		return cfg, errors.New("at least one --subject is required (or pass --config)")
	}
	return cfg, nil
}

func kvConfigFromFlags(cmd *cobra.Command, name string) (jetstream.KeyValueConfig, error) {
	cfg := jetstream.KeyValueConfig{Bucket: name}

	if path, _ := cmd.Flags().GetString("config"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("read --config: %w", err)
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".yaml", ".yml":
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return cfg, fmt.Errorf("parse %s: %w", path, err)
			}
		default:
			if err := json.Unmarshal(data, &cfg); err != nil {
				return cfg, fmt.Errorf("parse %s: %w", path, err)
			}
		}
		cfg.Bucket = name
	}

	if cmd.Flags().Changed("history") {
		v, _ := cmd.Flags().GetUint8("history")
		cfg.History = v
	}
	if cmd.Flags().Changed("ttl") {
		v, _ := cmd.Flags().GetDuration("ttl")
		cfg.TTL = v
	}
	if cmd.Flags().Changed("max-bytes") {
		v, _ := cmd.Flags().GetInt64("max-bytes")
		cfg.MaxBytes = v
	}
	if cmd.Flags().Changed("max-value-size") {
		v, _ := cmd.Flags().GetInt32("max-value-size")
		cfg.MaxValueSize = v
	}
	if cmd.Flags().Changed("storage") {
		v, _ := cmd.Flags().GetString("storage")
		s, err := parseStorage(v)
		if err != nil {
			return cfg, err
		}
		cfg.Storage = s
	}
	if cmd.Flags().Changed("replicas") {
		v, _ := cmd.Flags().GetInt("replicas")
		cfg.Replicas = v
	}
	return cfg, nil
}

func parseRetention(s string) (jetstream.RetentionPolicy, error) {
	switch strings.ToLower(s) {
	case "limits", "limit", "":
		return jetstream.LimitsPolicy, nil
	case "interest":
		return jetstream.InterestPolicy, nil
	case "workqueue", "work-queue":
		return jetstream.WorkQueuePolicy, nil
	}
	return 0, fmt.Errorf("invalid --retention %q (limits|interest|workqueue)", s)
}

func parseStorage(s string) (jetstream.StorageType, error) {
	switch strings.ToLower(s) {
	case "file", "":
		return jetstream.FileStorage, nil
	case "memory", "mem":
		return jetstream.MemoryStorage, nil
	}
	return 0, fmt.Errorf("invalid --storage %q (file|memory)", s)
}

func storageName(s jetstream.StorageType) string {
	switch s {
	case jetstream.FileStorage:
		return "file"
	case jetstream.MemoryStorage:
		return "memory"
	}
	return fmt.Sprintf("%d", int(s))
}

// structToRecord converts any JSON-marshalable value into a pb.Record so we
// can reuse the existing record/list printers.
func structToRecord(v any) (pb.Record, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var r pb.Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return r, nil
}

func confirm(format string, args ...any) bool {
	fmt.Fprintf(os.Stderr, format, args...)
	var line string
	fmt.Scanln(&line)
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y")
}

func init() {
	// stream flags
	jsStreamCreateCmd.Flags().StringSlice("subject", nil, "subject(s) the stream captures (repeat or comma-separate)")
	jsStreamCreateCmd.Flags().Int64("max-msgs", 0, "max messages (0 = unlimited)")
	jsStreamCreateCmd.Flags().Int64("max-bytes", 0, "max bytes (0 = unlimited)")
	jsStreamCreateCmd.Flags().Duration("max-age", 0, "max message age (e.g. 24h)")
	jsStreamCreateCmd.Flags().String("retention", "limits", "retention policy: limits|interest|workqueue")
	jsStreamCreateCmd.Flags().String("storage", "file", "storage: file|memory")
	jsStreamCreateCmd.Flags().Int("replicas", 1, "number of replicas")
	jsStreamCreateCmd.Flags().String("config", "", "YAML/JSON jetstream.StreamConfig (overrides flags)")
	jsStreamPurgeCmd.Flags().BoolP("yes", "y", false, "skip confirmation")
	jsStreamDeleteCmd.Flags().BoolP("yes", "y", false, "skip confirmation")

	// bucket flags
	jsBucketCreateCmd.Flags().Uint8("history", 1, "history depth (1-64)")
	jsBucketCreateCmd.Flags().Duration("ttl", 0, "per-key TTL (0 = no expiry)")
	jsBucketCreateCmd.Flags().Int64("max-bytes", 0, "max bytes (0 = unlimited)")
	jsBucketCreateCmd.Flags().Int32("max-value-size", 0, "max value size in bytes (0 = unlimited)")
	jsBucketCreateCmd.Flags().String("storage", "file", "storage: file|memory")
	jsBucketCreateCmd.Flags().Int("replicas", 1, "number of replicas")
	jsBucketCreateCmd.Flags().String("config", "", "YAML/JSON jetstream.KeyValueConfig (overrides flags)")
	jsBucketDeleteCmd.Flags().BoolP("yes", "y", false, "skip confirmation")

	jsStreamCmd.AddCommand(jsStreamLsCmd, jsStreamInfoCmd, jsStreamCreateCmd, jsStreamPurgeCmd, jsStreamDeleteCmd)
	jsBucketCmd.AddCommand(jsBucketLsCmd, jsBucketInfoCmd, jsBucketCreateCmd, jsBucketDeleteCmd)
	jsCmd.AddCommand(jsStreamCmd, jsBucketCmd)
	rootCmd.AddCommand(jsCmd)
}
