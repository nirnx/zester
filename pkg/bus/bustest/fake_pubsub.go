package bustest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/nats-io/nats.go"

	"github.com/nirnx/zester/pkg/bus"
)

// FakePubSub implements bus.PubSub and bus.RequestPubSub with in-memory
// message routing. Supports NATS subject wildcards: * (one token) and
// > (rest of subject), queue-group subscriptions (round-robin delivery per
// group), and synchronous request/reply.
type FakePubSub struct {
	mu       sync.Mutex
	subs     []*fakeSub
	rr       map[string]int // queue group name -> round-robin counter
	inboxSeq atomic.Uint64
}

// Compile-time check: FakePubSub provides the full request/reply capability.
var _ bus.RequestPubSub = (*FakePubSub)(nil)

// NewFakePubSub creates a new in-memory pub/sub fake.
func NewFakePubSub() *FakePubSub {
	return &FakePubSub{}
}

func (ps *FakePubSub) Publish(subject string, data []byte) error {
	ps.deliver(bus.NewMsg(subject, data, "", nil))
	return nil
}

// deliver routes msg to all matching plain subscribers plus exactly one
// member of each matching queue group (round-robin). Handlers are invoked
// synchronously. Returns the number of handlers invoked.
func (ps *FakePubSub) deliver(msg *bus.Msg) int {
	ps.mu.Lock()
	var targets []*fakeSub
	queues := make(map[string][]*fakeSub)
	for _, s := range ps.subs {
		if !s.active.Load() || !matchSubject(s.subject, msg.Subject) {
			continue
		}
		if s.queue == "" {
			targets = append(targets, s)
		} else {
			queues[s.queue] = append(queues[s.queue], s)
		}
	}
	for q, members := range queues {
		if ps.rr == nil {
			ps.rr = make(map[string]int)
		}
		i := ps.rr[q] % len(members)
		ps.rr[q]++
		targets = append(targets, members[i])
	}
	ps.mu.Unlock()

	for _, s := range targets {
		s.handler(msg)
	}
	return len(targets)
}

func (ps *FakePubSub) Subscribe(subject string, handler func(msg *bus.Msg)) (bus.Subscription, error) {
	return ps.subscribe(subject, "", handler)
}

// QueueSubscribe subscribes as a member of the named queue group. Each
// published message matching subject is delivered to exactly one member of
// the group, rotating round-robin between members.
func (ps *FakePubSub) QueueSubscribe(subject, queue string, handler func(msg *bus.Msg)) (bus.Subscription, error) {
	if queue == "" {
		return nil, fmt.Errorf("bustest: queue name must not be empty")
	}
	return ps.subscribe(subject, queue, handler)
}

func (ps *FakePubSub) subscribe(subject, queue string, handler func(msg *bus.Msg)) (bus.Subscription, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	s := &fakeSub{
		subject: subject,
		queue:   queue,
		handler: handler,
	}
	s.active.Store(true)
	ps.subs = append(ps.subs, s)
	return s, nil
}

// Request publishes data on subject with a reply inbox and waits for the
// first response. Handlers run synchronously during the call, so a handler
// that responds inline completes the request without blocking. Returns
// nats.ErrNoResponders when no subscription matches the subject, mirroring
// production NATS behavior.
func (ps *FakePubSub) Request(ctx context.Context, subject string, data []byte) (*bus.Msg, error) {
	reply := fmt.Sprintf("_INBOX.fake.%d", ps.inboxSeq.Add(1))
	replyCh := make(chan *bus.Msg, 1)
	req := bus.NewMsg(subject, data, reply, func(resp []byte) error {
		select {
		case replyCh <- bus.NewMsg(reply, resp, "", nil):
		default: // only the first reply wins
		}
		return nil
	})

	if ps.deliver(req) == 0 {
		return nil, nats.ErrNoResponders
	}

	select {
	case m := <-replyCh:
		return m, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// --- fakeSub implements bus.Subscription ---

type fakeSub struct {
	subject string
	queue   string // "" for plain subscriptions
	handler func(msg *bus.Msg)
	active  atomic.Bool
}

func (s *fakeSub) Unsubscribe() error {
	s.active.Store(false)
	return nil
}

// matchSubject matches a NATS subject pattern against a concrete subject.
// Supports * (single token) and > (rest of subject).
func matchSubject(pattern, subject string) bool {
	patParts := strings.Split(pattern, ".")
	subParts := strings.Split(subject, ".")

	for i, pat := range patParts {
		if pat == ">" {
			// NATS semantics: > must match at least one remaining token,
			// so "a.>" matches "a.b" but not the bare "a".
			return i < len(subParts)
		}
		if i >= len(subParts) {
			return false
		}
		if pat != "*" && pat != subParts[i] {
			return false
		}
	}
	return len(patParts) == len(subParts)
}
