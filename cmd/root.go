package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	// version is set via -ldflags at build time.
	version = "dev"

	// Persistent flags available on every command.
	flagContext string
	flagOutput  string
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
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&flagContext, "context", "", "context name to use (overrides active context)")
	rootCmd.PersistentFlags().StringVarP(&flagOutput, "output", "o", "", "output format: table | json | yaml")

	// Subcommands are wired up in their own init() funcs by importing them
	// indirectly via this package; see individual command files.
}
