package natsx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/synadia-io/orbit.go/natscontext"
)

// Connect dials NATS using the nats-cli context named on the stone context.
//
// stone resolves the context file itself and hands natscontext.Connect an
// absolute path, so the two can never disagree about where contexts live.
//
// An unset or missing nats context is an error, not a fall-through to whatever
// context the user last selected with `nats context select`. That fallback is
// what made a bad setup look like a working one: stone would connect to an
// unrelated server (or to localhost, when no context was selected at all),
// report "listening", and never deliver a message.
func Connect(c ctx.Context, opts ...nats.Option) (*nats.Conn, natscontext.Settings, error) {
	path, err := ResolveContextFile(c.NATSContext)
	if err != nil {
		return nil, natscontext.Settings{}, err
	}
	nc, settings, err := natscontext.Connect(path, append(defaultOptions(), opts...)...)
	if err != nil {
		return nil, natscontext.Settings{}, fmt.Errorf("nats connect (context=%s): %w", contextLabel(path), err)
	}
	return nc, settings, nil
}

// defaultOptions apply to every connection stone makes. The handlers matter:
// without them nats.go swallows async server errors, so a "Permissions
// Violation for Subscription to ..." — the usual reason a sub sees nothing —
// never reaches the user.
func defaultOptions() []nats.Option {
	return []nats.Option{
		nats.Name("stone"),
		nats.ErrorHandler(func(_ *nats.Conn, s *nats.Subscription, err error) {
			if s != nil {
				fmt.Fprintf(os.Stderr, "nats error: %v (subject %q)\n", err, s.Subject)
				return
			}
			fmt.Fprintf(os.Stderr, "nats error: %v\n", err)
		}),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				fmt.Fprintf(os.Stderr, "nats: disconnected: %v\n", err)
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			fmt.Fprintf(os.Stderr, "nats: reconnected to %s\n", nc.ConnectedUrl())
		}),
	}
}

// contextLabel turns a resolved context path back into its context name.
func contextLabel(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// JetStream returns a JetStream handle, honoring the context's JS domain.
func JetStream(nc *nats.Conn, settings natscontext.Settings) (jetstream.JetStream, error) {
	if settings.JSDomain != "" {
		return jetstream.NewWithDomain(nc, settings.JSDomain)
	}
	return jetstream.New(nc)
}
