package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"
	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/stone-age-io/stone-cli/internal/natsx"
	"github.com/synadia-io/orbit.go/natscontext"
)

var natsCmd = &cobra.Command{
	Use:   "nats",
	Short: "Publish, subscribe, and request against NATS",
	Long: `NATS commands connect with the nats-cli context named by 'nats_context' on
the active stone context, which 'stone org switch' and 'stone nats sync-context'
generate. Pass --nats-context <name> to use a different one.

If no context is set, stone stops rather than falling back to whichever context
'nats context select' happens to point at — that fallback silently connects to
an unrelated server.`,
}

// natsConnect is the canonical way commands dial NATS. It applies the
// persistent --nats-context override.
func natsConnect(c ctx.Context, opts ...nats.Option) (*nats.Conn, natscontext.Settings, error) {
	if flagNATSContext != "" {
		c.NATSContext = flagNATSContext
	}
	return natsx.Connect(c, opts...)
}

var natsPubCmd = &cobra.Command{
	Use:   "pub <subject> <payload>",
	Short: "Publish a message to a subject",
	Long: `Publish a payload to a subject.

  payload         positional argument
  @<path>         read payload from a file
  -               read payload from stdin

With --js, the publish goes through JetStream and prints the ack
(stream, sequence, duplicate).`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx.Active(flagContext)
		if err != nil {
			return err
		}
		payload, err := readPayload(args[1])
		if err != nil {
			return err
		}
		useJS, _ := cmd.Flags().GetBool("js")

		nc, settings, err := natsConnect(c)
		if err != nil {
			return err
		}
		defer nc.Drain()

		if useJS {
			js, err := natsx.JetStream(nc, settings)
			if err != nil {
				return err
			}
			pctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ack, err := js.Publish(pctx, args[0], payload)
			if err != nil {
				return err
			}
			fmt.Printf("ack: stream=%s seq=%d duplicate=%t\n", ack.Stream, ack.Sequence, ack.Duplicate)
			return nil
		}
		if err := nc.Publish(args[0], payload); err != nil {
			return err
		}
		return nc.Flush()
	},
}

var natsSubCmd = &cobra.Command{
	Use:   "sub <subject>",
	Short: "Subscribe to a subject and print messages until Ctrl-C",
	Long: `Subscribe to a subject and print messages as they arrive.

This is a core NATS subscription: it shows messages published from now on, not
messages already stored in a JetStream stream. To read what a stream already
holds, use 'stone js stream view <stream>'.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx.Active(flagContext)
		if err != nil {
			return err
		}
		jsonOut, _ := cmd.Flags().GetBool("json")

		nc, _, err := natsConnect(c)
		if err != nil {
			return err
		}
		defer nc.Drain()

		sub, err := nc.Subscribe(args[0], func(m *nats.Msg) {
			if jsonOut {
				fmt.Printf(`{"subject":%q,"reply":%q,"data":%q}`+"\n", m.Subject, m.Reply, string(m.Data))
				return
			}
			fmt.Printf("%s  %s\n", m.Subject, string(m.Data))
		})
		if err != nil {
			return err
		}
		// Push the SUB to the server and wait for the round trip before
		// claiming to be listening: a subject the creds can't read comes back
		// as a permissions violation, which the connection's error handler
		// prints to stderr.
		if err := nc.Flush(); err != nil {
			return fmt.Errorf("subscribe to %q: %w", args[0], err)
		}
		if !sub.IsValid() {
			return fmt.Errorf("subscription to %q was rejected by the server (see the error above)", args[0])
		}
		fmt.Fprintf(os.Stderr, "listening on %q via %s (Ctrl-C to stop)\n", args[0], nc.ConnectedUrl())
		waitForSignal()
		return nil
	},
}

var natsReqCmd = &cobra.Command{
	Use:   "req <subject> <payload>",
	Short: "Send a request and print the reply",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx.Active(flagContext)
		if err != nil {
			return err
		}
		payload, err := readPayload(args[1])
		if err != nil {
			return err
		}
		timeout, _ := cmd.Flags().GetDuration("timeout")

		nc, _, err := natsConnect(c)
		if err != nil {
			return err
		}
		defer nc.Drain()
		m, err := nc.Request(args[0], payload, timeout)
		if err != nil {
			return err
		}
		os.Stdout.Write(m.Data)
		if !strings.HasSuffix(string(m.Data), "\n") {
			fmt.Println()
		}
		return nil
	},
}

func readPayload(arg string) ([]byte, error) {
	switch {
	case arg == "-":
		return io.ReadAll(os.Stdin)
	case strings.HasPrefix(arg, "@"):
		return os.ReadFile(arg[1:])
	default:
		return []byte(arg), nil
	}
}

func waitForSignal() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}

var natsSyncContextCmd = &cobra.Command{
	Use:   "sync-context",
	Short: "Regenerate the nats-cli context for the current organization",
	Long: `Re-fetches the current user's membership and nats_user record for the
active organization, then writes a fresh .creds file and nats-cli context.

Useful after rotating keys ('stone nats-user update <id> --regenerate')
or if the local files have drifted.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx.Active(flagContext)
		if err != nil {
			return err
		}
		if c.Auth.Token == "" {
			return errors.New("not logged in. run: stone auth login")
		}
		if c.CurrentOrganization == "" {
			return errors.New("no current organization. run: stone org switch <name|id>")
		}
		if natsURL, _ := cmd.Flags().GetString("nats-url"); natsURL != "" {
			c.NATSURL = natsURL
			_ = ctx.Save(c)
		}
		if c.NATSURL == "" {
			return errors.New("no NATS URL set on this stone context. pass --nats-url or run: stone context create --nats-url ...")
		}
		setDefault, _ := cmd.Flags().GetBool("set-nats-default")
		verbose, _ := cmd.Flags().GetBool("verbose")
		client := newPBClient(c)

		// Best-effort: fetch org name for the description.
		var orgName string
		if rec, err := client.Get("organizations", c.CurrentOrganization); err == nil {
			orgName, _ = rec["name"].(string)
		}

		res, skipReason, err := syncNATSContextForCurrentOrg(client, c, c.CurrentOrganization, orgName, setDefault, verbose)
		if err != nil {
			return err
		}
		if skipReason != "" {
			return fmt.Errorf("nothing to sync — %s", skipReason)
		}
		if res.Name != c.NATSContext {
			c.NATSContext = res.Name
			if err := ctx.Save(c); err != nil {
				return fmt.Errorf("nats-context written but failed to update stone context: %w", err)
			}
		}
		fmt.Printf("nats-context: %s\n", res.Name)
		fmt.Printf("context:      %s\n", res.CtxPath)
		fmt.Printf("creds:        %s\n", res.CredsPath)
		if res.RemovedPath != "" {
			fmt.Printf("removed:      %s (stale copy nats-cli could not see)\n", res.RemovedPath)
		}
		if setDefault {
			fmt.Println("set as nats-cli default")
		}
		return nil
	},
}

func init() {
	natsPubCmd.Flags().Bool("js", false, "publish through JetStream and print the ack")
	natsSubCmd.Flags().Bool("json", false, "emit each message as a JSON line")
	natsReqCmd.Flags().Duration("timeout", 2*time.Second, "request timeout")

	natsSyncContextCmd.Flags().Bool("set-nats-default", false, "also set the generated context as the nats-cli default")
	natsSyncContextCmd.Flags().String("nats-url", "", "NATS server URL (persists onto the stone context)")
	natsSyncContextCmd.Flags().Bool("verbose", false, "print diagnostic details (user_id, membership_id, creds length)")

	natsCmd.AddCommand(natsPubCmd, natsSubCmd, natsReqCmd, natsSyncContextCmd)
	rootCmd.AddCommand(natsCmd)
}
