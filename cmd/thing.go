package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/stone-age-io/stone-cli/internal/ctx"
)

// `thing provision` wraps POST /api/org/things, the platform's transactional
// create. It sits beside the generated `thing create` rather than replacing it,
// because the two answer different questions: `create` writes one inventory
// record and nothing else, which is what a workspace apply does; `provision`
// stands a device up on the network in one shot.
//
// WHY THE ROUTE EXISTS, and why the CLI should prefer it. Minting a Thing with
// its NATS identity and Nebula host used to be three independent client calls
// with no rollback, so a failure on the third left a signed NATS credential and
// an allocated overlay IP behind with nothing referencing either. The route runs
// all three inside one PocketBase transaction, and that atomicity is real rather
// than decorative: pb-nats signs and publishes on AfterCreateSuccess, which
// PocketBase defers to transaction completion, so a rollback means NATS was
// never told anything.
//
// It also needs two authority levels for one operation, which no create rule can
// express: a member may add inventory, but attaching an identity to it is
// owner/admin. things.createRule draws that line by freezing nats_user and
// nebula_host for the member branch; the route restates it as a role check per
// section.
//
// The email and both passwords are server-side. The Thing's email is
// <code>@<org-code>.thing.local -- a synthetic address that exists so the
// (organization, code) join key has somewhere to live, not so anyone can write
// to it -- and the password comes back exactly once, because PocketBase stores
// only its hash.
var thingProvisionCmd = &cobra.Command{
	Use:   "provision",
	Short: "Create a thing and mint its NATS and Nebula identities in one transaction",
	Long: `Stand a device up: one Thing, optionally its NATS identity and its Nebula host,
all in a single server-side transaction.

Prefer this over ` + "`thing create`" + ` for a real device. ` + "`create`" + ` writes an inventory
row and leaves the identities to you, which means three separate writes with
nothing to undo the first two if the third fails -- and a half-provisioned device
leaves a signed NATS credential and an allocated overlay IP owned by nothing.

The Thing's email and password are generated server-side. The password is
printed ONCE and never stored in retrievable form; record it now or reset it
later.

--code is optional. Leave it out and the server generates one under the Thing
Type's prefix, like CA-9KD-4PX, and prints it with the rest. A code stencilled
on the hardware (DOOR-1) is still the right one to pass. Either way it is
frozen once the Thing exists, as is --type.

Each identity has three modes:

    none    (default) provision nothing.
    auto    mint a new one. NATS uses the organization's active account and its
            default role unless --nats-role names another. Nebula needs
            --nebula-network and --nebula-ip; the route does not allocate an
            address for you.
    link    attach an identity that already exists, by id.

Attaching either identity requires owner or admin. A member may provision a
Thing with both modes left at none.

The organization is taken from your active context, never from the request, so
this cannot be aimed at another tenant.

Examples:

  # inventory only, no identities; the server generates the code
  stone thing provision --name "Probe 07" --type <thing_types_id>

  # a gateway with a fresh NATS identity and a fresh Nebula host
  stone thing provision --code gw-01 --name "Gateway 01" \
    --type <thing_types_id> --location <locations_id> \
    --nats-mode auto \
    --nebula-mode auto --nebula-network <id> --nebula-ip 10.128.0.42`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx.Active(flagContext)
		if err != nil {
			return err
		}

		natsMode, _ := cmd.Flags().GetString("nats-mode")
		nebulaMode, _ := cmd.Flags().GetString("nebula-mode")
		for flag, mode := range map[string]string{"nats-mode": natsMode, "nebula-mode": nebulaMode} {
			if !containsString([]string{"auto", "link", "none"}, mode) {
				return fmt.Errorf("--%s must be one of: auto, link, none", flag)
			}
		}

		name, _ := cmd.Flags().GetString("name")
		code, _ := cmd.Flags().GetString("code")
		description, _ := cmd.Flags().GetString("description")
		thingType, _ := cmd.Flags().GetString("type")
		location, _ := cmd.Flags().GetString("location")
		metadataRaw, _ := cmd.Flags().GetString("metadata")

		natsUser, _ := cmd.Flags().GetString("nats-user")
		natsRole, _ := cmd.Flags().GetString("nats-role")
		nebulaHost, _ := cmd.Flags().GetString("nebula-host")
		nebulaNetwork, _ := cmd.Flags().GetString("nebula-network")
		nebulaIP, _ := cmd.Flags().GetString("nebula-ip")

		// Checked here as well as on the server so a typo costs a round trip
		// rather than a transaction. The server still checks: it owns the rule,
		// and these are the same conditions restated, not a substitute.
		if natsMode == "link" && natsUser == "" {
			return fmt.Errorf("--nats-user is required when --nats-mode is link")
		}
		if nebulaMode == "link" && nebulaHost == "" {
			return fmt.Errorf("--nebula-host is required when --nebula-mode is link")
		}
		if nebulaMode == "auto" && (nebulaNetwork == "" || nebulaIP == "") {
			return fmt.Errorf("--nebula-network and --nebula-ip are both required when --nebula-mode is auto")
		}

		metadata, err := readJSONValue(metadataRaw)
		if err != nil {
			return fmt.Errorf("--metadata: %w", err)
		}

		body := map[string]any{
			"name":        name,
			"code":        code,
			"description": description,
			"type":        thingType,
			"location":    location,
			"nats": map[string]any{
				"mode":    natsMode,
				"user_id": natsUser,
				"role_id": natsRole,
			},
			"nebula": map[string]any{
				"mode":       nebulaMode,
				"host_id":    nebulaHost,
				"network_id": nebulaNetwork,
				"overlay_ip": nebulaIP,
			},
		}
		if metadata != nil {
			body["metadata"] = metadata
		}

		var out struct {
			ID       string `json:"id"`
			Code     string `json:"code"`
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := newPBClient(c).CallRoute("/api/org/things", body, &out); err != nil {
			return err
		}

		fmt.Printf("provisioned thing %s (%s)\n", out.Code, out.ID)
		fmt.Printf("  email:    %s\n", out.Email)
		fmt.Printf("  password: %s\n", out.Password)
		fmt.Println("\nThe password is shown once and is not retrievable. Record it now.")
		if natsMode != "none" || nebulaMode != "none" {
			fmt.Printf("\nIdentities: stone thing get %s\n", out.Code)
		}
		return nil
	},
}

// extraCommands are subcommands attached to a generated entity tree that are not
// record CRUD. registerCRUD consults this map, so `thing provision` lands under
// `thing`, and `invite accept` under `invite`, without entity.go knowing what
// either one is.
//
// A package-level var rather than something an init() populates, and the
// difference is not stylistic: Go initializes every package-level variable
// before any init() runs, but init() functions run in FILE-NAME order, and
// entity.go's -- the one that calls registerCRUD -- sorts before invite.go's
// and thing.go's. A command appended from one of those init()s would be
// attached to nothing, silently, and the subcommand would simply not exist.
// Flags are added in init() and that is fine: AddCommand only links the
// command, and nothing parses until Execute().
var extraCommands = map[string][]*cobra.Command{
	"thing":  {thingProvisionCmd},
	"invite": {inviteAcceptCmd},
}

func init() {
	f := thingProvisionCmd.Flags()
	f.String("name", "", "display name (required)")
	f.String("code", "", "stable short code, unique within the organization ignoring case; omit it to have one generated under the type's prefix")
	f.String("description", "", "free-form description")
	f.String("type", "", "thing_types id; its prefix goes into a generated code (frozen once set)")
	f.String("location", "", "locations id")
	f.String("metadata", "", "arbitrary JSON metadata (inline JSON, @file, or -)")

	f.String("nats-mode", "none", "NATS identity: auto, link, or none")
	f.String("nats-user", "", "nats_users id to attach (--nats-mode link)")
	f.String("nats-role", "", "nats_roles id for the new identity (--nats-mode auto; defaults to the org's default role)")

	f.String("nebula-mode", "none", "Nebula host: auto, link, or none")
	f.String("nebula-host", "", "nebula_hosts id to attach (--nebula-mode link)")
	f.String("nebula-network", "", "nebula_networks id for the new host (--nebula-mode auto)")
	f.String("nebula-ip", "", "overlay IP for the new host (--nebula-mode auto; not allocated for you)")

	_ = thingProvisionCmd.MarkFlagRequired("name")
}
