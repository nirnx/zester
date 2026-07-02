package bustest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ptorbus/zester/pkg/bus"
)

func TestFakePubSubQueueSubscribeRoundRobin(t *testing.T) {
	ps := NewFakePubSub()

	var mu sync.Mutex
	counts := map[string]int{}
	handler := func(name string) func(msg *bus.Msg) {
		return func(msg *bus.Msg) {
			mu.Lock()
			counts[name]++
			mu.Unlock()
		}
	}

	if _, err := ps.QueueSubscribe("svc.echo", "workers", handler("a")); err != nil {
		t.Fatalf("queue subscribe a: %v", err)
	}
	if _, err := ps.QueueSubscribe("svc.echo", "workers", handler("b")); err != nil {
		t.Fatalf("queue subscribe b: %v", err)
	}
	// Plain subscriber receives everything regardless of queue delivery.
	if _, err := ps.Subscribe("svc.echo", handler("plain")); err != nil {
		t.Fatalf("subscribe plain: %v", err)
	}

	for i := 0; i < 4; i++ {
		if err := ps.Publish("svc.echo", []byte("x")); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if counts["a"]+counts["b"] != 4 {
		t.Fatalf("queue group received %d messages, want 4", counts["a"]+counts["b"])
	}
	if counts["a"] != 2 || counts["b"] != 2 {
		t.Fatalf("expected round-robin 2/2, got a=%d b=%d", counts["a"], counts["b"])
	}
	if counts["plain"] != 4 {
		t.Fatalf("plain subscriber received %d messages, want 4", counts["plain"])
	}
}

func TestFakePubSubQueueSubscribeEmptyQueue(t *testing.T) {
	ps := NewFakePubSub()
	if _, err := ps.QueueSubscribe("svc", "", func(msg *bus.Msg) {}); err == nil {
		t.Fatal("expected error for empty queue name")
	}
}

func TestFakePubSubRequestReply(t *testing.T) {
	ps := NewFakePubSub()

	_, err := ps.QueueSubscribe("svc.add", "workers", func(msg *bus.Msg) {
		if msg.Reply == "" {
			t.Error("expected reply subject on request message")
		}
		if err := msg.Respond(append([]byte("re:"), msg.Data...)); err != nil {
			t.Errorf("respond: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	resp, err := ps.Request(ctx, "svc.add", []byte("hello"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if string(resp.Data) != "re:hello" {
		t.Fatalf("unexpected reply payload: %q", resp.Data)
	}
}

func TestFakePubSubRequestNoResponders(t *testing.T) {
	ps := NewFakePubSub()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := ps.Request(ctx, "svc.nobody", []byte("hi"))
	if !errors.Is(err, nats.ErrNoResponders) {
		t.Fatalf("expected nats.ErrNoResponders, got %v", err)
	}
}

func TestFakePubSubRequestUnsubscribedIsNoResponder(t *testing.T) {
	ps := NewFakePubSub()

	sub, err := ps.Subscribe("svc.gone", func(msg *bus.Msg) {
		_ = msg.Respond([]byte("nope"))
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err = ps.Request(ctx, "svc.gone", []byte("hi"))
	if !errors.Is(err, nats.ErrNoResponders) {
		t.Fatalf("expected nats.ErrNoResponders after unsubscribe, got %v", err)
	}
}

func TestFakePubSubRequestQueueGroupSingleDelivery(t *testing.T) {
	ps := NewFakePubSub()

	var mu sync.Mutex
	handled := 0
	for i := 0; i < 3; i++ {
		_, err := ps.QueueSubscribe("svc.q", "workers", func(msg *bus.Msg) {
			mu.Lock()
			handled++
			mu.Unlock()
			_ = msg.Respond([]byte("ok"))
		})
		if err != nil {
			t.Fatalf("queue subscribe: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if _, err := ps.Request(ctx, "svc.q", []byte("hi")); err != nil {
		t.Fatalf("request: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if handled != 1 {
		t.Fatalf("request handled by %d queue members, want 1", handled)
	}
}
