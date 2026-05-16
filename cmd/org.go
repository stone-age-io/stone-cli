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
		client := newPBClient(c)
		items, err := client.ListAll("organizations", pb.ListOptions{Sort: "name"})
		if err != nil {
			return err
		}
		// Tag the active org so callers can identify it in any format.
		for _, it := range items {
			id, _ := it["id"].(string)
			it["current"] = id == c.CurrentOrganization
		}
		out := resolveOutput()
		if out == "json" || out == "yaml" {
			return pb.PrintList(os.Stdout, items, []string{"id", "name", "current"}, out)
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
		client := newPBClient(c)
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

		client := newPBClient(c)
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
			fmt.Println("nats-sync: skipped (--no-nats)")
			return nil
		}
		natsURL, _ := cmd.Flags().GetString("nats-url")
		if natsURL != "" && natsURL != c.NATSURL {
			c.NATSURL = natsURL
			_ = ctx.Save(c)
		}
		if c.NATSURL == "" {
			fmt.Println("nats-sync: skipped — no NATS URL on this stone context")
			fmt.Println("           set one with: stone org switch <org> --nats-url nats://host:4222")
			return nil
		}
		setDefault, _ := cmd.Flags().GetBool("set-nats-default")
		verbose, _ := cmd.Flags().GetBool("verbose")
		res, skipReason, err := syncNATSContextForCurrentOrg(client, c, orgID, orgName, setDefault, verbose)
		if err != nil {
			fmt.Fprintf(os.Stderr, "nats-sync: FAILED — %v\n", err)
			return nil
		}
		if skipReason != "" {
			fmt.Printf("nats-sync: skipped — %s\n", skipReason)
			return nil
		}
		if res.Name != c.NATSContext {
			c.NATSContext = res.Name
			if err := ctx.Save(c); err != nil {
				return fmt.Errorf("nats-context written but failed to update stone context: %w", err)
			}
		}
		fmt.Printf("nats-sync: wrote context %q\n", res.Name)
		fmt.Printf("           context: %s\n", res.CtxPath)
		fmt.Printf("           creds:   %s\n", res.CredsPath)
		if setDefault {
			fmt.Println("           set as nats-cli default (~/.config/nats/context.txt)")
		}
		return nil
	},
}

// syncNATSContextForCurrentOrg looks up the current user's membership for
// orgID, fetches its nats_user, and writes a per-org nats-cli context.
// Returns (result, skipReason, error). skipReason is non-empty when we
// intentionally did nothing (e.g., user has no membership for this org).
func syncNATSContextForCurrentOrg(client *pb.Client, c ctx.Context, orgID, orgName string, setDefault, verbose bool) (natsx.SyncResult, string, error) {
	if verbose {
		fmt.Fprintf(os.Stderr, "[verbose] user_id=%s org_id=%s org_name=%q nats_url=%s\n", c.Auth.UserID, orgID, orgName, c.NATSURL)
	}
	m, err := natsx.MembershipForOrg(client, c.Auth.Collection, c.Auth.UserID, orgID)
	if err != nil {
		return natsx.SyncResult{}, "", fmt.Errorf("lookup membership: %w", err)
	}
	if m == nil {
		return natsx.SyncResult{}, "no membership found for this user+org (operators must be members of an org to get NATS creds)", nil
	}
	if verbose {
		mid, _ := m["id"].(string)
		fmt.Fprintf(os.Stderr, "[verbose] membership_id=%s\n", mid)
	}
	nu, err := natsx.FetchNATSUserForMembership(client, m)
	if err != nil {
		return natsx.SyncResult{}, "", fmt.Errorf("fetch nats_user: %w", err)
	}
	if nu == nil {
		return natsx.SyncResult{}, "membership has no linked nats_user (the platform's hooks may not have provisioned one yet)", nil
	}
	if verbose {
		nid, _ := nu["id"].(string)
		uname, _ := nu["nats_username"].(string)
		creds, _ := nu["creds_file"].(string)
		fmt.Fprintf(os.Stderr, "[verbose] nats_user_id=%s nats_username=%q creds_file_len=%d\n", nid, uname, len(creds))
	}
	res, err := natsx.SyncContextForOrg(natsx.SyncOptions{
		StoneContext: c.Name,
		OrgID:        orgID,
		OrgName:      orgName,
		NATSURL:      c.NATSURL,
		NATSUser:     nu,
		SetSelected:  setDefault,
	})
	return res, "", err
}

// escapePBString escapes double quotes for use inside a PocketBase filter literal.
func escapePBString(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}

func init() {
	orgSwitchCmd.Flags().Bool("no-nats", false, "skip generating a nats-cli context for the new org")
	orgSwitchCmd.Flags().Bool("set-nats-default", false, "also set the generated context as the nats-cli default (writes ~/.config/nats/context.txt)")
	orgSwitchCmd.Flags().String("nats-url", "", "NATS server URL to use (persists onto the stone context)")
	orgSwitchCmd.Flags().Bool("verbose", false, "print diagnostic details about the nats-sync step")

	orgCmd.AddCommand(orgLsCmd, orgCurrentCmd, orgSwitchCmd)
	rootCmd.AddCommand(orgCmd)
}
