package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/stone-age-io/stone-cli/internal/pb"
)

var (
	// version is set via -ldflags at build time.
	version = "dev"

	// Persistent flags available on every command.
	flagContext     string
	flagOutput      string
	flagDebug       bool
	flagNATSContext string
)

var rootCmd = &cobra.Command{
	Use:           "stone",
	Short:         "Opinionated CLI for the Stone Age IoT Platform",
	Long:          "stone is a command-line client for the Stone Age IoT Platform.\nIt manages tenant resources, syncs declarative workspaces, and talks NATS\nusing the platform's auth.",
	Version:       version,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the root command and exits with an appropriate status code.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		var exit exitError
		if errors.As(err, &exit) {
			os.Exit(exit.Code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&flagContext, "context", "", "context name to use (overrides active context)")
	rootCmd.PersistentFlags().StringVarP(&flagOutput, "output", "o", "", "output format: table | json | yaml")
	rootCmd.PersistentFlags().BoolVar(&flagDebug, "debug", false, "log HTTP requests and responses to stderr")
	rootCmd.PersistentFlags().StringVar(&flagNATSContext, "nats-context", "", "nats-cli context to connect with (overrides the stone context's nats_context)")

	// Subcommands are wired up in their own init() funcs by importing them
	// indirectly via this package; see individual command files.
}

// newPBClient is the canonical way commands construct a PocketBase client.
// It applies the persistent --debug flag so every HTTP call is logged when
// the user asks for it.
func newPBClient(c ctx.Context) *pb.Client {
	cl := pb.New(c)
	cl.Debug = flagDebug
	return cl
}

// exitError ends the process with Code and prints nothing more: the command
// has already said what it had to. It is for an exit status that is an
// answer rather than a failure, like --exit-code's 2 for "changes pending".
type exitError struct{ Code int }

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// errChangesPending is what a --dry-run with --exit-code returns when apply
// would send something: 2, as `git diff --exit-code` and `terraform plan
// -detailed-exitcode` use it, so 1 still means an error.
var errChangesPending = exitError{Code: 2}
