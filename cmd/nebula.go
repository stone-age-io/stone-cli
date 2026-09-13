package cmd

import (
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"
	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/stone-age-io/stone-cli/internal/pb"
)

// The two Nebula operations that are routes rather than record writes, for the
// same two reasons the NATS ones in creds.go are.
//
// ca-rotate exists because nebula_ca.updateRule is operator-only. Permitting the
// rotation trigger through an update rule would mean a deny-list over the
// certificate, the private key, the incoming and outgoing CA material, the
// expiry and the curve -- and that deny-list would silently open up the moment
// anyone added a field, on the record holding the trust anchor for the whole
// tenant's mesh.
//
// cert-audit exists for a different reason: answering it means parsing a Nebula
// certificate to compare the network it carries against the network the host
// belongs to. No client can do that, so the platform does it and hands back the
// verdict.

var nebulaCmd = &cobra.Command{
	Use:   "nebula",
	Short: "Nebula overlay operations that are not record writes",
	Long: `Nebula operations the collection API cannot express.

The records themselves are managed with the entity commands -- nebula-ca,
nebula-network and nebula-host. What lives here is the CA rotation lever, which
nebula_ca.updateRule deliberately does not expose, and the host certificate
audit, which needs a certificate parsed to answer.`,
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

var nebulaCertAuditCmd = &cobra.Command{
	Use:   "cert-audit",
	Short: "List hosts whose certificate no longer matches their network",
	Long: `Report active hosts whose certificate carries the wrong overlay network.

pb-nebula signed host certificates at /32 until v0.3.0. Nebula does not read a
certificate's network as "this host's address" -- it puts the prefix straight
onto the tun device and installs a link route for it, so the mask in the
certificate IS the host's route to the overlay. A /32 gives a host a route
covering only itself: the certificate verifies, the config renders, the host
starts, the handshake completes, and no packet ever crosses the mesh.

Nothing errors anywhere, which is why this needs asking for rather than waiting
to be told. A host's overlay_ip being edited after issue lands it here too.

Nothing is re-signed automatically, and that restraint is deliberate: re-signing
moves a certificate's fingerprint, and a fingerprint is what the revocation
blocklist matches, so a sweep would rewrite every peer config in the mesh. Fix
one host at a time, redeploying each config as you go:

    stone nebula-host update <hostname> --renew

Inactive hosts are excluded. They are revoked, and re-signing one would publish a
new fingerprint while the old certificate stayed valid and unblocklisted.

Owner or admin of the active organization.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx.Active(flagContext)
		if err != nil {
			return err
		}
		client := newPBClient(c)

		var out struct {
			Stale []string `json:"stale"`
		}
		if err := client.GetRoute("/api/org/nebula/cert-audit", &out); err != nil {
			return err
		}

		if len(out.Stale) == 0 {
			fmt.Println("no host certificate is out of step with its network")
			return nil
		}

		// The route answers in ids, because that is what it can be sure of. An
		// id is not what anyone types, so resolve to the hostname and network
		// the operator will actually act on -- and fall back to the bare id
		// rather than dropping a row if a lookup fails, since a host we cannot
		// name is still a host that needs re-signing.
		stale := map[string]bool{}
		for _, id := range out.Stale {
			stale[id] = true
		}

		hosts, err := client.ListAll("nebula_hosts", pb.ListOptions{
			Fields: "id,hostname,overlay_ip,network_id",
			Sort:   "hostname",
		})
		if err != nil {
			hosts = nil
		}

		named := map[string]pb.Record{}
		for _, h := range hosts {
			if id, _ := h["id"].(string); stale[id] {
				named[id] = h
			}
		}

		rows := make([]pb.Record, 0, len(out.Stale))
		for _, id := range out.Stale {
			if h, ok := named[id]; ok {
				rows = append(rows, h)
				continue
			}
			rows = append(rows, pb.Record{"id": id, "hostname": "(not readable)"})
		}
		sort.Slice(rows, func(i, j int) bool {
			a, _ := rows[i]["hostname"].(string)
			b, _ := rows[j]["hostname"].(string)
			return a < b
		})

		if err := pb.PrintList(os.Stdout, rows, []string{"hostname", "overlay_ip", "network_id"}, resolveOutput()); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "\n%d host(s) need re-issuing: stone nebula-host update <hostname> --renew\n", len(rows))
		return nil
	},
}

func init() {
	nebulaCARotateCmd.AddCommand(nebulaCARotatePrepareCmd, nebulaCARotateCommitCmd, nebulaCARotateFinishCmd)
	nebulaCmd.AddCommand(nebulaCARotateCmd, nebulaCertAuditCmd)
	rootCmd.AddCommand(nebulaCmd)
}
