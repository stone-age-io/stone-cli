package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/stone-age-io/stone-cli/internal/pb"
)

// The two credential operations that are routes rather than record writes.
//
// Both exist for the same reason: a PocketBase API rule cannot express a
// single-field allowlist. Permitting one of these through an update rule would
// mean asserting `:isset = false` on every other writable field — a deny-list
// that silently opens up the moment someone adds a field. And the fields that
// must stay closed are consequential: `nats_users.publish_permissions` is copied
// verbatim into the JWT the platform signs, and `nats_accounts` carries the
// account limits and the signed account JWT.
//
// So each is a route that takes no record id — the target is derived from the
// caller's own identity or active organization, so it cannot be aimed at anyone
// else — and writes exactly one field per action.

var natsCredsCmd = &cobra.Command{
	Use:   "creds",
	Short: "Manage your own NATS credential",
}

var natsCredsRotateCmd = &cobra.Command{
	Use:   "rotate",
	Short: "Rotate your own NATS credential",
	Long: `Rotate the NATS credential linked to your own identity.

Available to every role, including badge, and to things and leaf_nodes as well
as users. Takes no id: the route derives the target from your auth token, so it
can only ever rotate your own credential.

Rotation is not revocation — the previous credential stays valid until it
expires or an owner/admin revokes it. After a suspected compromise, revoke:

    stone nats-user update <username> --revoke`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx.Active(flagContext)
		if err != nil {
			return err
		}
		client := newPBClient(c)

		var out struct {
			Rotated  bool   `json:"rotated"`
			NatsUser string `json:"nats_user"`
		}
		if err := client.CallRoute("/api/me/nats-creds/rotate", nil, &out); err != nil {
			return err
		}

		fmt.Printf("rotated NATS credential %s\n", out.NatsUser)
		fmt.Println("re-issue the local creds file with: stone nats sync-context")
		return nil
	},
}

var natsAccountKeysCmd = &cobra.Command{
	Use:   "account-keys",
	Short: "Manage your organization's NATS account signing keys",
	Long: `Manage the signing keys on your active organization's NATS account.

These are routes rather than record writes because nats_accounts.updateRule is
operator-only: the record mixes fields a tenant may legitimately trigger with
the account limits it was sold and the signed account JWT. Owner/admin only.

Reach for add-signing for routine rotation. rotate is for suspected key
compromise — it invalidates every user JWT in the account at once.`,
}

var natsAccountKeysAddCmd = &cobra.Command{
	Use:   "add-signing",
	Short: "Append a new signing key (graceful; existing user JWTs stay valid)",
	Args:  cobra.NoArgs,
	RunE:  func(cmd *cobra.Command, args []string) error { return accountKeyAction("add_signing", "") },
}

var natsAccountKeysRemoveCmd = &cobra.Command{
	Use:   "remove-signing <public-key>",
	Short: "Remove one signing key by public key (the last one cannot be removed)",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return accountKeyAction("remove_signing", args[0]) },
}

var natsAccountKeysRotateCmd = &cobra.Command{
	Use:   "rotate",
	Short: "Emergency replacement: purge every signing key and generate one",
	Long: `Purge every signing key on the account and generate a single new one.

This invalidates EVERY user JWT in the account — every credential must be
re-minted before its holder can connect again. It is the response to a suspected
key compromise, not a routine operation; for routine rotation use add-signing,
which leaves existing user JWTs valid.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error { return accountKeyAction("rotate", "") },
}

// accountKeyAction posts one action to the account key route. The account is
// derived server-side from the caller's active organization, so there is no id
// to pass and no way to aim this at another tenant.
func accountKeyAction(action, publicKey string) error {
	c, err := ctx.Active(flagContext)
	if err != nil {
		return err
	}
	client := newPBClient(c)

	body := map[string]any{"action": action}
	if publicKey != "" {
		body["public_key"] = publicKey
	}

	var out pb.Record
	if err := client.CallRoute("/api/org/nats-account/keys", body, &out); err != nil {
		return err
	}
	return pb.PrintRecord(os.Stdout, out, resolveOutput())
}

func init() {
	natsCredsCmd.AddCommand(natsCredsRotateCmd)
	natsAccountKeysCmd.AddCommand(natsAccountKeysAddCmd, natsAccountKeysRemoveCmd, natsAccountKeysRotateCmd)
	// natsCmd is a package-level var, so it is initialized before any init()
	// in the package runs — attaching here is safe regardless of file order.
	natsCmd.AddCommand(natsCredsCmd, natsAccountKeysCmd)
}
