package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/stone-age-io/stone-cli/internal/ctx"
)

// The Nebula operation that is a route rather than a record write.
//
// ca-rotate exists because nebula_ca.updateRule is operator-only. Permitting the
// rotation trigger through an update rule would mean a deny-list over the
// certificate, the private key, the incoming and outgoing CA material, the
// expiry and the curve -- and that deny-list would silently open up the moment
// anyone added a field, on the record holding the trust anchor for the whole
// tenant's mesh.

var nebulaCmd = &cobra.Command{
	Use:   "nebula",
	Short: "Nebula overlay operations that are not record writes",
	Long: `Nebula operations the collection API cannot express.

The records themselves are managed with the entity commands -- nebula-ca,
nebula-network and nebula-host. What lives here is the CA rotation lever, which
nebula_ca.updateRule deliberately does not expose.`,
}

var nebulaCARotateCmd = &cobra.Command{
	Use:   "ca-rotate",
	Short: "Rotate your organization's Nebula CA, in three steps",
	Long: `Roll the Nebula CA your organization's host certificates chain to.

Three steps, and the wait between them is the feature.

Nebula verification is mutual -- each peer checks the other against its OWN local
CA pool, with no chain and no fallback -- and hosts pull their config whenever
they like, with nothing telling the platform when they did. So one write carrying
both the new trust bundle and the new certificate splits the mesh: a host that
has fetched presents a new-CA certificate to one that has not, and the handshake
fails in BOTH directions until propagation finishes.

    prepare   mint the incoming CA and publish it as additional trust.
              Issuance does not move, no host certificate changes and no
              fingerprint moves, so this is fully reversible.

              -- wait here until every host has fetched its config --

    commit    switch issuance to the new CA and re-sign every ACTIVE host.
              Both CAs stay trusted, so re-signed and not-yet-re-signed hosts
              still talk to each other. Idempotent: re-running it re-signs only
              the hosts still on the outgoing CA, which is how you recover from a
              partial sweep.

              -- deploy the new config to every active host --

    finish    drop the outgoing CA. Refused while any active host still holds a
              certificate signed by it; the error names the host.

How long to wait in the middle is your judgement and nothing can compress it.
A CA cannot be renewed, only rotated, so start months before expiry rather than
weeks -- see the expiry on: stone nebula-ca ls

Owner or admin of the active organization. Takes no id: the CA is derived from
your active organization, so this cannot be aimed at another tenant.`,
}

var nebulaCARotatePrepareCmd = &cobra.Command{
	Use:   "prepare",
	Short: "Mint the incoming CA and publish trust in it (reversible)",
	Args:  cobra.NoArgs,
	RunE:  func(cmd *cobra.Command, args []string) error { return rotateCAStep("prepare") },
}

var nebulaCARotateCommitCmd = &cobra.Command{
	Use:   "commit",
	Short: "Switch issuance to the new CA and re-sign every active host",
	Long: `Switch issuance to the prepared CA and re-sign every active host.

Run this only once every host has fetched the config prepare published -- until
then, some peers do not yet trust the CA the re-signed hosts now present.

Inactive hosts are deliberately NOT re-signed: an inactive host is revoked, its
fingerprint sits in every peer's blocklist, and re-signing it would publish a new
fingerprint while the old certificate stayed valid.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error { return rotateCAStep("commit") },
}

var nebulaCARotateFinishCmd = &cobra.Command{
	Use:   "finish",
	Short: "Drop the outgoing CA (refused while any active host still uses it)",
	Long: `Drop the outgoing CA from every host's trust bundle.

Refused while any active host still holds a certificate signed by it, and the
refusal names the host. That interlock is what makes this step safe to expose:
dropping the outgoing CA early does not fail loudly, it just takes that host off
the mesh -- it keeps running, and its peers quietly stop being able to verify it.

If it refuses, deploy the config to the host it names and try again.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error { return rotateCAStep("finish") },
}

// rotateCAStep posts one step to the rotation route. The CA is derived
// server-side from the caller's active organization, so there is no id to pass.
func rotateCAStep(step string) error {
	c, err := ctx.Active(flagContext)
	if err != nil {
		return err
	}
	client := newPBClient(c)

	var out struct {
		Applied  string `json:"applied"`
		NebulaCA string `json:"nebula_ca"`
	}
	// The server refuses an out-of-order step and names the host blocking a
	// finish. Those messages are the whole point of the interlock, so they are
	// returned as-is rather than replaced with something tidier.
	if err := client.CallRoute("/api/org/nebula-ca/rotate", map[string]any{"step": step}, &out); err != nil {
		return err
	}

	fmt.Printf("%s: applied to CA %s\n", out.Applied, out.NebulaCA)
	switch step {
	case "prepare":
		fmt.Println("every host now trusts the incoming CA. Let them all fetch their config, then: stone nebula ca-rotate commit")
	case "commit":
		fmt.Println("active hosts have been re-signed. Deploy their configs, then: stone nebula ca-rotate finish")
	case "finish":
		fmt.Println("the outgoing CA has been dropped. Rotation complete.")
	}
	return nil
}

func init() {
	nebulaCARotateCmd.AddCommand(nebulaCARotatePrepareCmd, nebulaCARotateCommitCmd, nebulaCARotateFinishCmd)
	nebulaCmd.AddCommand(nebulaCARotateCmd)
	rootCmd.AddCommand(nebulaCmd)
}
