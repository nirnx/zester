package bus

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// JetStreamAPI is the subset of jetstream.JetStream that Zester uses, with
// KV operations returning the bus-owned KV interface. Production code passes
// the JS adapter (bus.NewJS / Client.JetStream()); tests pass an in-memory
// fake from the bustest package.
type JetStreamAPI interface {
	CreateOrUpdateKeyValue(ctx context.Context, cfg jetstream.KeyValueConfig) (KV, error)
	KeyValue(ctx context.Context, bucket string) (KV, error)
	DeleteKeyValue(ctx context.Context, bucket string) error
	CreateStream(ctx context.Context, cfg jetstream.StreamConfig) (jetstream.Stream, error)
	Publish(ctx context.Context, subject string, payload []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error)
}

// Msg carries the payload for a pub/sub message.
type Msg struct {
	Subject string
	Data    []byte
	Reply   string
	respond func([]byte) error
}

// NewMsg constructs a Msg with an optional responder function. It is intended
// for PubSub implementations (including test fakes) that need to wire
// request/reply flows: Msg.Respond invokes the given respond function.
// A nil respond leaves the message without reply capability.
func NewMsg(subject string, data []byte, reply string, respond func([]byte) error) *Msg {
	return &Msg{Subject: subject, Data: data, Reply: reply, respond: respond}
}

// Respond sends a response for request/reply flows when available.
func (m *Msg) Respond(data []byte) error {
	if m.respond == nil {
		return fmt.Errorf("bus: no reply subject on message")
	}
	return m.respond(data)
}

// Subscription represents an active subscription that can be unsubscribed.
type Subscription interface {
	Unsubscribe() error
}

// PubSub is a publish/subscribe interface used by the job package.
// Production code uses NATSPubSub; tests use an in-memory fake.
type PubSub interface {
	Publish(subject string, data []byte) error
	Subscribe(subject string, handler func(msg *Msg)) (Subscription, error)
}

// RequestPubSub extends PubSub with request/reply and queue-group
// subscription capabilities. Both NATSPubSub (production) and
// bustest.FakePubSub (tests) satisfy it. Consumers that need these
// capabilities should accept a PubSub and type-assert to RequestPubSub,
// so existing third-party PubSub implementations keep compiling unchanged.
type RequestPubSub interface {
	PubSub

	// Request publishes data on subject and waits for a single reply,
	// honoring ctx cancellation/deadline. When nothing subscribes to the
	// subject, implementations return an error (nats.ErrNoResponders in
	// production, matched via errors.Is).
	Request(ctx context.Context, subject string, data []byte) (*Msg, error)

	// QueueSubscribe subscribes as a member of the named queue group:
	// each matching message is delivered to exactly one member of the group.
	QueueSubscribe(subject, queue string, handler func(msg *Msg)) (Subscription, error)
}

// NATSPubSub adapts *nats.Conn to the PubSub interface.
type NATSPubSub struct {
	nc *nats.Conn
}

// NewNATSPubSub wraps a NATS connection as a PubSub.
func NewNATSPubSub(nc *nats.Conn) *NATSPubSub {
	return &NATSPubSub{nc: nc}
}

func (n *NATSPubSub) Publish(subject string, data []byte) error {
	return n.nc.Publish(subject, data)
}

func (n *NATSPubSub) Subscribe(subject string, handler func(msg *Msg)) (Subscription, error) {
	return n.nc.Subscribe(subject, func(m *nats.Msg) {
		handler(&Msg{
			Subject: m.Subject,
			Data:    m.Data,
			Reply:   m.Reply,
			respond: m.Respond,
		})
	})
}

// Request publishes data on subject and waits for a single reply. Returns an
// error wrapping nats.ErrNoResponders when nothing is subscribed to subject.
func (n *NATSPubSub) Request(ctx context.Context, subject string, data []byte) (*Msg, error) {
	m, err := n.nc.RequestWithContext(ctx, subject, data)
	if err != nil {
		return nil, fmt.Errorf("bus: request %s: %w", subject, err)
	}
	return &Msg{
		Subject: m.Subject,
		Data:    m.Data,
		Reply:   m.Reply,
		respond: m.Respond,
	}, nil
}

// QueueSubscribe subscribes to subject as a member of the given queue group.
func (n *NATSPubSub) QueueSubscribe(subject, queue string, handler func(msg *Msg)) (Subscription, error) {
	return n.nc.QueueSubscribe(subject, queue, func(m *nats.Msg) {
		handler(&Msg{
			Subject: m.Subject,
			Data:    m.Data,
			Reply:   m.Reply,
			respond: m.Respond,
		})
	})
}

// Compile-time check: NATSPubSub provides the full request/reply capability.
var _ RequestPubSub = (*NATSPubSub)(nil)
