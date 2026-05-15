package cmd

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/stone-age-io/stone-cli/internal/natsx"
	"github.com/stone-age-io/stone-cli/internal/pb"
)

var orgCmd = &cobra.Command{
	Use:   "org",
	Short: "Manage and switch organizations",
	Long:  "Organization context for the active CLI context. 'switch' updates both\nthe server-side users.current_organization field and the local sidecar.",
}

var orgLsCmd = &cobra.Command{
	Use:     "ls",
	Aliases: []string{"list"},
	Short:   "List organizations visible to the current user",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx.Active(flagContext)
		if err != nil {
			return err
		}
		client := pb.New(c)
		items, err := client.ListAll("organizations", pb.ListOptions{Sort: "name"})
		if err != nil {
			return err
		}
		if len(items) == 0 {
			fmt.Println("no organizations visible to this user")
			return nil
		}
		for _, it := range items {
			id, _ := it["id"].(string)
			name, _ := it["name"].(string)
			marker := "  "
			if id == c.CurrentOrganization {
				marker = "* "
			}
			fmt.Printf("%s%s  %s\n", marker, id, name)
		}
		return nil
	},
}

var orgCurrentCmd = &cobra.Command{
	Use:   "current",
	Short: "Show the current organization (local cache)",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx.Active(flagContext)
		if err != nil {
			return err
		}
		if c.CurrentOrganization == "" {
			return errors.New("no current organization set. run: stone org switch <name|id>")
		}
		// Best-effort enrich with name from server.
		client := pb.New(c)
		rec, err := client.Get("organizations", c.CurrentOrganization)
		if err != nil {
			fmt.Println(c.CurrentOrganization)
			return nil
		}
		name, _ := rec["name"].(string)
		fmt.Printf("%s  %s\n", c.CurrentOrganization, name)
		return nil
	},
}

// pocketbaseIDRE matches PocketBase's 15-char alphanumeric record IDs.
var pocketbaseIDRE = regexp.MustCompile(`^[A-Za-z0-9]{15}$`)

var orgSwitchCmd = &cobra.Command{
	Use:   "switch <name|id>",
	Short: "Switch the current organization (updates server and local cache)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx.Active(flagContext)
		if err != nil {
			return err
		}
		if c.Auth.Token == "" {
			return errors.New("not logged in. run: stone auth login")
		}
		if c.Auth.UserID == "" {
			// Recover from older contexts that didn't capture user_id at login.
			id, err := pb.DecodeJWTUserID(c.Auth.Token)
			if err != nil {
				return fmt.Errorf("cannot determine user id: %w", err)
			}
			c.Auth.UserID = id
		}

		client := pb.New(c)
		target := args[0]
		orgID := target
		var orgName string
		if !pocketbaseIDRE.MatchString(target) {
			// Resolve name to id.
			filter := fmt.Sprintf(`name="%s"`, escapePBString(target))
			lr, err := client.List("organizations", pb.ListOptions{Filter: filter, PerPage: 2})
			if err != nil {
				return err
			}
			switch len(lr.Items) {
			case 0:
				return fmt.Errorf("no organization matches name %q", target)
			case 1:
				orgID, _ = lr.Items[0]["id"].(string)
				orgName, _ = lr.Items[0]["name"].(string)
			default:
				return fmt.Errorf("multiple organizations match name %q; use id instead", target)
			}
		} else {
			// Sanity-check the id and grab the name for the success line.
			rec, err := client.Get("organizations", target)
			if err != nil {
				return fmt.Errorf("organization id %q not accessible: %w", target, err)
			}
			orgName, _ = rec["name"].(string)
		}

		if _, err := client.Update(c.Auth.Collection, c.Auth.UserID, pb.Record{"current_organization": orgID}); err != nil {
			return fmt.Errorf("update users.current_organization: %w", err)
		}
		c.CurrentOrganization = orgID
		if err := ctx.Save(c); err != nil {
			return fmt.Errorf("saved on server but failed to write local sidecar: %w (org id: %s)", err, orgID)
		}
		fmt.Printf("switched to %s (%s)\n", or(orgName, "(unnamed)"), orgID)

		// --- NATS context sync ----------------------------------------
		skipNats, _ := cmd.Flags().GetBool("no-nats")
		if skipNats {
			return nil
		}
		natsURL, _ := cmd.Flags().GetString("nats-url")
		if natsURL != "" && natsURL != c.NATSURL {
			c.NATSURL = natsURL
			_ = ctx.Save(c)
		}
		if c.NATSURL == "" {
			fmt.Fprintln(os.Stderr, "note: no NATS URL set on this stone context; skipping nats-context sync.\n      set with: stone org switch <org> --nats-url nats://host:4222")
			return nil
		}
		setDefault, _ := cmd.Flags().GetBool("set-nats-default")
		natsName, err := syncNATSContextForCurrentOrg(client, c, orgID, orgName, setDefault)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: nats-context sync failed: %v\n", err)
			return nil
		}
		if natsName == "" {
			return nil
		}
		if natsName != c.NATSContext {
			c.NATSContext = natsName
			if err := ctx.Save(c); err != nil {
				return fmt.Errorf("nats-context written but failed to update stone context: %w", err)
			}
		}
		fmt.Printf("nats-context: %s\n", natsName)
		if setDefault {
			fmt.Printf("set as nats-cli default\n")
		}
		return nil
	},
}

// syncNATSContextForCurrentOrg looks up the current user's membership for
// orgID, fetches its nats_user, and writes a per-org nats-cli context. It
// returns the nats-cli context name (empty if there is no membership/nats
// user for this org).
func syncNATSContextForCurrentOrg(client *pb.Client, c ctx.Context, orgID, orgName string, setDefault bool) (string, error) {
	m, err := natsx.MembershipForOrg(client, c.Auth.Collection, c.Auth.UserID, orgID)
	if err != nil {
		return "", fmt.Errorf("lookup membership: %w", err)
	}
	if m == nil {
		fmt.Fprintln(os.Stderr, "note: no membership found for this user+org; skipping nats-context sync.")
		return "", nil
	}
	nu, err := natsx.FetchNATSUserForMembership(client, m)
	if err != nil {
		return "", fmt.Errorf("fetch nats_user: %w", err)
	}
	if nu == nil {
		fmt.Fprintln(os.Stderr, "note: membership has no linked nats_user; skipping nats-context sync.")
		return "", nil
	}
	return natsx.SyncContextForOrg(natsx.SyncOptions{
		StoneContext: c.Name,
		OrgID:        orgID,
		OrgName:      orgName,
		NATSURL:      c.NATSURL,
		NATSUser:     nu,
		SetSelected:  setDefault,
	})
}

// escapePBString escapes double quotes for use inside a PocketBase filter literal.
func escapePBString(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}

func init() {
	orgSwitchCmd.Flags().Bool("no-nats", false, "skip generating a nats-cli context for the new org")
	orgSwitchCmd.Flags().Bool("set-nats-default", false, "also set the generated context as the nats-cli default (writes ~/.config/nats/context.txt)")
	orgSwitchCmd.Flags().String("nats-url", "", "NATS server URL to use (persists onto the stone context)")

	orgCmd.AddCommand(orgLsCmd, orgCurrentCmd, orgSwitchCmd)
	rootCmd.AddCommand(orgCmd)
}
