package cmd

import (
	"context"
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
)

var natsCmd = &cobra.Command{
	Use:   "nats",
	Short: "Publish, subscribe, and request against NATS",
	Long: `NATS commands use the nats-cli context configured on the stone context
(via 'nats_context' in the context file) or the user's default nats-cli context
when unset.`,
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

		nc, settings, err := natsx.Connect(c)
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
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := ctx.Active(flagContext)
		if err != nil {
			return err
		}
		jsonOut, _ := cmd.Flags().GetBool("json")

		nc, _, err := natsx.Connect(c)
		if err != nil {
			return err
		}
		defer nc.Drain()

		_, err = nc.Subscribe(args[0], func(m *nats.Msg) {
			if jsonOut {
				fmt.Printf(`{"subject":%q,"reply":%q,"data":%q}`+"\n", m.Subject, m.Reply, string(m.Data))
				return
			}
			fmt.Printf("%s  %s\n", m.Subject, string(m.Data))
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "listening on %q (Ctrl-C to stop)\n", args[0])
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

		nc, _, err := natsx.Connect(c)
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
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
}

func init() {
	natsPubCmd.Flags().Bool("js", false, "publish through JetStream and print the ack")
	natsSubCmd.Flags().Bool("json", false, "emit each message as a JSON line")
	natsReqCmd.Flags().Duration("timeout", 2*time.Second, "request timeout")

	natsCmd.AddCommand(natsPubCmd, natsSubCmd, natsReqCmd)
	rootCmd.AddCommand(natsCmd)
}
