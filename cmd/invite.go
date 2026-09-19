package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/stone-age-io/stone-cli/internal/ctx"
)

// `invite accept` wraps POST /api/org/invites/accept.
//
// WHY IT IS A ROUTE. Redeeming an invitation writes a memberships record on
// behalf of someone who is not yet in the organization -- there is no rule that
// could admit that write, because every tenant rule is expressed in terms of a
// membership the caller does not have yet. The route matches the token to the
// caller's own email address instead, and does the whole thing in one
// transaction: create the membership, set current_organization if it was blank,
// delete the invitation.
//
// WHY THE CLI NEEDS IT. Every other side of the invitation flow is already here
// -- `stone invite create` issues one, `stone invite ls` shows what is
// outstanding -- but the redeeming end was reachable only through the console.
// That left the CLI unable to complete the one flow it could start, which
// matters most for an operator or an automation account joining a tenant it was
// invited to.
//
// It takes the token and nothing else. The organization is the one the
// invitation names, and the caller is whoever the token in the active context
// says they are, so there is no id here either.
var inviteAcceptCmd = &cobra.Command{
	Use:   "accept <token>",
	Short: "Redeem an invitation token and join the organization",
	Long: `Redeem an invitation token for the account you are logged in as.

The invitation is matched to you by email address, case-insensitively, so this
only ever accepts an invitation that was issued to you. The token is the value
in the invitation link -- the ?token= parameter -- not the invite record's id.

Your current organization is set to the new one only if you did not already have
one; joining a second organization does not move you out of the one you are
working in. Use ` + "`stone org switch`" + ` for that.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx.Active(flagContext)
		if err != nil {
			return err
		}

		var out struct {
			Message       string `json:"message"`
			Organization  string `json:"organization"`
			AlreadyMember bool   `json:"alreadyMember"`
		}
		if err := newPBClient(c).CallRoute("/api/org/invites/accept", map[string]any{"token": args[0]}, &out); err != nil {
			return err
		}

		fmt.Println(out.Message)
		if out.AlreadyMember {
			// The route answers 200 for this: a double-clicked link is not an
			// error, and the invitation is deleted either way.
			return nil
		}
		if out.Organization != "" {
			fmt.Printf("organization: %s\n", out.Organization)
		}
		// The local context is a cache of the server's current_organization, and
		// the route may have just set it. Rather than guess which way it went,
		// point at the command that reconciles both -- and that also syncs the
		// NATS context, which a fresh membership has no creds for until it runs.
		fmt.Println("\nnext: stone org switch <name|id>   (sets the active org and syncs its NATS context)")
		return nil
	},
}

// No init() here on purpose: the command is attached through the extraCommands
// literal in thing.go, which is a package-level var. An init() in this file runs
// AFTER entity.go's, which is the one that reads that map.
