package natsx

import (
	"fmt"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stone-age-io/stone-cli/internal/ctx"
	"github.com/synadia-io/orbit.go/natscontext"
)

// Connect uses orbit.go's natscontext to dial NATS using the named nats-cli
// context. An empty name resolves to the user's default nats-cli context.
func Connect(c ctx.Context, opts ...nats.Option) (*nats.Conn, natscontext.Settings, error) {
	opts = append(opts, nats.Name("stone"))
	nc, settings, err := natscontext.Connect(c.NATSContext, opts...)
	if err != nil {
		return nil, natscontext.Settings{}, fmt.Errorf("nats connect (context=%q): %w", c.NATSContext, err)
	}
	return nc, settings, nil
}

// JetStream returns a JetStream handle, honoring the context's JS domain.
func JetStream(nc *nats.Conn, settings natscontext.Settings) (jetstream.JetStream, error) {
	if settings.JSDomain != "" {
		return jetstream.NewWithDomain(nc, settings.JSDomain)
	}
	return jetstream.New(nc)
}
