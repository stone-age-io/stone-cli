package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/spf13/cobra"
	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/stone-age-io/stone-cli/internal/natsx"
)

var kvCmd = &cobra.Command{
	Use:   "kv",
	Short: "Read and write JetStream Key/Value buckets",
}

var kvGetCmd = &cobra.Command{
	Use:   "get <bucket> <key>",
	Short: "Print the value of a key",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		kv, cleanup, err := openKV(args[0])
		if err != nil {
			return err
		}
		defer cleanup()
		entry, err := kv.Get(context.Background(), args[1])
		if err != nil {
			return err
		}
		os.Stdout.Write(entry.Value())
		if !strings.HasSuffix(string(entry.Value()), "\n") {
			fmt.Println()
		}
		return nil
	},
}

var kvPutCmd = &cobra.Command{
	Use:   "put <bucket> <key> <value>",
	Short: "Set a key's value",
	Long: `Set the value of a key. The value may be:
  literal text     positional argument
  @<path>          read value from a file
  -                read value from stdin`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		value, err := readPayload(args[2])
		if err != nil {
			return err
		}
		kv, cleanup, err := openKV(args[0])
		if err != nil {
			return err
		}
		defer cleanup()
		rev, err := kv.Put(context.Background(), args[1], value)
		if err != nil {
			return err
		}
		fmt.Printf("rev=%d\n", rev)
		return nil
	},
}

var kvDelCmd = &cobra.Command{
	Use:     "del <bucket> <key>",
	Aliases: []string{"delete", "rm"},
	Short:   "Delete a key (preserves history)",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		kv, cleanup, err := openKV(args[0])
		if err != nil {
			return err
		}
		defer cleanup()
		return kv.Delete(context.Background(), args[1])
	},
}

var kvLsCmd = &cobra.Command{
	Use:     "ls <bucket>",
	Aliases: []string{"list", "keys"},
	Short:   "List keys in a bucket",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		kv, cleanup, err := openKV(args[0])
		if err != nil {
			return err
		}
		defer cleanup()
		ctx2, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		keys, err := kv.Keys(ctx2)
		if err != nil {
			return err
		}
		for _, k := range keys {
			fmt.Println(k)
		}
		return nil
	},
}

var kvWatchCmd = &cobra.Command{
	Use:   "watch <bucket> [<key-pattern>]",
	Short: "Stream updates to a bucket (Ctrl-C to stop)",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		kv, cleanup, err := openKV(args[0])
		if err != nil {
			return err
		}
		defer cleanup()

		pattern := ">"
		if len(args) == 2 {
			pattern = args[1]
		}
		w, err := kv.Watch(context.Background(), pattern)
		if err != nil {
			return err
		}
		defer w.Stop()

		fmt.Fprintf(os.Stderr, "watching %s/%s (Ctrl-C to stop)\n", args[0], pattern)
		go func() {
			for entry := range w.Updates() {
				if entry == nil {
					continue // initial values done
				}
				fmt.Printf("%-8s %s/%s rev=%d  %s\n",
					strings.ToUpper(entry.Operation().String()),
					entry.Bucket(), entry.Key(), entry.Revision(), string(entry.Value()))
			}
		}()
		waitForSignal()
		return nil
	},
}

func openKV(bucket string) (jetstream.KeyValue, func(), error) {
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
	ctx2, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := js.KeyValue(ctx2, bucket)
	if err != nil {
		nc.Drain()
		return nil, nil, err
	}
	return store, func() { nc.Drain() }, nil
}

func init() {
	kvCmd.AddCommand(kvGetCmd, kvPutCmd, kvDelCmd, kvLsCmd, kvWatchCmd)
	rootCmd.AddCommand(kvCmd)
}
