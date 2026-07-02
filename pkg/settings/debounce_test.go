package settings

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestDebouncedFunc_CoalescesRapidCalls(t *testing.T) {
	var count atomic.Int32
	d := NewDebouncedFunc(DebouncedFuncConfig{
		Fn:       func() { count.Add(1) },
		Debounce: 100 * time.Millisecond,
		Jitter:   1, // effectively zero but non-default
	})
	defer d.Stop()

	// Fire 5 times in quick succession.
	for range 5 {
		d.Trigger()
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for debounce to fire.
	time.Sleep(250 * time.Millisecond)

	if got := count.Load(); got != 1 {
		t.Errorf("expected 1 invocation, got %d", got)
	}
}

func TestDebouncedFunc_JitterDelaysExecution(t *testing.T) {
	var called atomic.Int32
	var recordedJitter time.Duration

	d := NewDebouncedFunc(DebouncedFuncConfig{
		Fn:       func() { called.Add(1) },
		Debounce: 50 * time.Millisecond,
		Jitter:   1 * time.Second,
	})
	defer d.Stop()

	// Inject afterFn that records the jitter duration and returns immediately.
	d.afterFn = func(dur time.Duration) <-chan time.Time {
		recordedJitter = dur
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}

	d.Trigger()
	time.Sleep(150 * time.Millisecond)

	if got := called.Load(); got != 1 {
		t.Fatalf("expected 1 invocation, got %d", got)
	}
	if recordedJitter < 0 || recordedJitter > 1*time.Second {
		t.Errorf("jitter %v not in [0, 1s)", recordedJitter)
	}
}

func TestDebouncedFunc_StopCancelsPending(t *testing.T) {
	var count atomic.Int32
	d := NewDebouncedFunc(DebouncedFuncConfig{
		Fn:       func() { count.Add(1) },
		Debounce: 100 * time.Millisecond,
		Jitter:   1,
	})

	d.Trigger()
	d.Stop()

	time.Sleep(250 * time.Millisecond)

	if got := count.Load(); got != 0 {
		t.Errorf("expected 0 invocations after Stop, got %d", got)
	}
}

func TestDebouncedFunc_SecondTriggerResetsTimer(t *testing.T) {
	var count atomic.Int32
	d := NewDebouncedFunc(DebouncedFuncConfig{
		Fn:       func() { count.Add(1) },
		Debounce: 200 * time.Millisecond,
		Jitter:   1,
	})
	defer d.Stop()

	d.Trigger()
	time.Sleep(100 * time.Millisecond)
	d.Trigger() // reset the timer

	time.Sleep(100 * time.Millisecond)
	// 200ms hasn't elapsed since second Trigger.
	if got := count.Load(); got != 0 {
		t.Errorf("expected 0 invocations at 100ms after reset, got %d", got)
	}

	time.Sleep(150 * time.Millisecond)
	// Now ~250ms after second Trigger — should have fired.
	if got := count.Load(); got != 1 {
		t.Errorf("expected 1 invocation after full debounce, got %d", got)
	}
}
