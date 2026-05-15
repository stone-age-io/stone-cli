package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stone-age-io/stone-cli/internal/ctx"
)

var contextCmd = &cobra.Command{
	Use:     "context",
	Aliases: []string{"ctx"},
	Short:   "Manage named environment contexts",
	Long:    "A context bundles a PocketBase URL, auth token, current organization, optional NATS context, and workspace path.",
}

var contextCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new context",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		url, _ := cmd.Flags().GetString("url")
		natsURL, _ := cmd.Flags().GetString("nats-url")
		setActive, _ := cmd.Flags().GetBool("use")
		if url == "" {
			return fmt.Errorf("--url is required")
		}
		if err := ctx.ValidateName(name); err != nil {
			return err
		}
		if _, err := ctx.Load(name); err == nil {
			return fmt.Errorf("context %q already exists", name)
		}
		c := ctx.Context{
			Name:    name,
			URL:     strings.TrimRight(url, "/"),
			NATSURL: strings.TrimSpace(natsURL),
		}
		if err := ctx.Save(c); err != nil {
			return err
		}
		fmt.Printf("created context %q -> %s\n", name, c.URL)
		// If no contexts existed before this, default to making it active.
		g, _ := ctx.LoadGlobal()
		if setActive || g.ActiveContext == "" {
			if err := ctx.SetActive(name); err != nil {
				return err
			}
			fmt.Printf("set %q as active\n", name)
		}
		return nil
	},
}

var contextUseCmd = &cobra.Command{
	Use:     "use <name>",
	Aliases: []string{"select"},
	Short:   "Set the active context",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := ctx.SetActive(args[0]); err != nil {
			return err
		}
		fmt.Printf("active context: %s\n", args[0])
		return nil
	},
}

var contextLsCmd = &cobra.Command{
	Use:     "ls",
	Aliases: []string{"list"},
	Short:   "List contexts",
	RunE: func(cmd *cobra.Command, args []string) error {
		names, err := ctx.List()
		if err != nil {
			return err
		}
		g, _ := ctx.LoadGlobal()
		if len(names) == 0 {
			fmt.Println("no contexts. run: stone context create <name> --url <url>")
			return nil
		}
		for _, n := range names {
			marker := "  "
			if n == g.ActiveContext {
				marker = "* "
			}
			fmt.Printf("%s%s\n", marker, n)
		}
		return nil
	},
}

var contextShowCmd = &cobra.Command{
	Use:   "show [name]",
	Short: "Show details of a context (default: active)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var name string
		if len(args) == 1 {
			name = args[0]
		}
		c, err := ctx.Active(name)
		if err != nil {
			return err
		}
		fmt.Printf("name:                 %s\n", c.Name)
		fmt.Printf("url:                  %s\n", c.URL)
		fmt.Printf("nats_url:             %s\n", or(c.NATSURL, "(unset)"))
		fmt.Printf("auth.collection:      %s\n", or(c.Auth.Collection, "(unset)"))
		fmt.Printf("auth.email:           %s\n", or(c.Auth.Email, "(unset)"))
		fmt.Printf("auth.expires:         %s\n", or(c.Auth.Expires, "(unset)"))
		fmt.Printf("current_organization: %s\n", or(c.CurrentOrganization, "(unset)"))
		fmt.Printf("nats_context:         %s\n", or(c.NATSContext, "(default)"))
		fmt.Printf("workspace:            %s\n", or(c.Workspace, "(unset)"))
		if c.Auth.Token != "" {
			fmt.Println("auth.token:           (set)")
		} else {
			fmt.Println("auth.token:           (unset)")
		}
		return nil
	},
}

var contextRmCmd = &cobra.Command{
	Use:     "rm <name>",
	Aliases: []string{"delete", "remove"},
	Short:   "Delete a context",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := ctx.Delete(name); err != nil {
			return err
		}
		g, _ := ctx.LoadGlobal()
		if g.ActiveContext == name {
			g.ActiveContext = ""
			_ = ctx.SaveGlobal(g)
		}
		fmt.Printf("deleted context %q\n", name)
		return nil
	},
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func init() {
	contextCreateCmd.Flags().String("url", "", "PocketBase base URL (e.g. https://host:8090)")
	contextCreateCmd.Flags().String("nats-url", "", "NATS server URL (e.g. nats://host:4222) used when generating per-org nats-cli contexts")
	contextCreateCmd.Flags().Bool("use", false, "make this the active context")

	contextCmd.AddCommand(contextCreateCmd, contextUseCmd, contextLsCmd, contextShowCmd, contextRmCmd)
	rootCmd.AddCommand(contextCmd)
}
