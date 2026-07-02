package masterd

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ptorbus/zester/internal/health"
)

// TestSchedConsumerRetryFlipsReadiness verifies that a boot failure of the
// scheduled-result consumer leaves the 'sched-consumer' readiness check
// Down, that the background retry loop flips it to OK once the consumer
// starts, and that the shutdown function stops the retried consumer.
func TestSchedConsumerRetryFlipsReadiness(t *testing.T) {
	var calls atomic.Int32
	var stopCalls atomic.Int32

	d := &Daemon{
		logger:             discardLogger(),
		checker:            health.New("test", time.Second),
		schedRetryInterval: 10 * time.Millisecond,
	}
	d.schedStart = func(context.Context) (func(), error) {
		if calls.Add(1) < 3 {
			return nil, errors.New("stream not ready")
		}
		return func() { stopCalls.Add(1) }, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := d.startSchedConsumer(ctx)
	if res := d.schedConsumerCheck(ctx); res.Status != health.StatusDown {
		t.Fatalf("expected Down after boot failure, got %+v", res)
	}

	waitFor(t, 2*time.Second, "sched-consumer readiness to flip to ok", func() bool {
		return d.schedConsumerCheck(ctx).Status == health.StatusOK
	})
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected 3 start attempts, got %d", got)
	}

	stop()
	if got := stopCalls.Load(); got != 1 {
		t.Fatalf("expected shutdown to stop the retried consumer once, got %d stop calls", got)
	}
}

// TestSchedConsumerBootSuccess verifies the happy path: the check is OK
// immediately and shutdown stops the boot-time consumer.
func TestSchedConsumerBootSuccess(t *testing.T) {
	var stopCalls atomic.Int32
	d := &Daemon{
		logger:  discardLogger(),
		checker: health.New("test", time.Second),
	}
	d.schedStart = func(context.Context) (func(), error) {
		return func() { stopCalls.Add(1) }, nil
	}

	ctx := context.Background()
	stop := d.startSchedConsumer(ctx)
	if res := d.schedConsumerCheck(ctx); res.Status != health.StatusOK {
		t.Fatalf("expected OK after boot success, got %+v", res)
	}
	stop()
	if got := stopCalls.Load(); got != 1 {
		t.Fatalf("expected 1 stop call, got %d", got)
	}
}

// TestSetSchedStopAfterShutdown verifies that a retry success landing after
// shutdown stops its own consumer instead of leaking it.
func TestSetSchedStopAfterShutdown(t *testing.T) {
	var stopCalls atomic.Int32
	d := &Daemon{logger: discardLogger()}

	d.shutdownSchedConsumer() // shutdown before any consumer registered
	if ok := d.setSchedStop(func() { stopCalls.Add(1) }); ok {
		t.Fatal("setSchedStop must report false after shutdown")
	}
	if got := stopCalls.Load(); got != 1 {
		t.Fatalf("late consumer must be stopped immediately, got %d stop calls", got)
	}
}
