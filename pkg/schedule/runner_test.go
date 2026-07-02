package schedule

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(t time.Time) *fakeClock {
	return &fakeClock{now: t}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

type fakeExec struct {
	mu      sync.Mutex
	calls   []fakeCall
	blockCh chan struct{} // if non-nil, blocks until closed
	result  ExecResult    // configurable result
}

type fakeCall struct {
	module string
	args   map[string]any
}

func newFakeExec() *fakeExec {
	return &fakeExec{
		result: ExecResult{Success: true},
	}
}

func (f *fakeExec) Exec(ctx context.Context, module string, args map[string]any) ExecResult {
	if f.blockCh != nil {
		select {
		case <-f.blockCh:
		case <-ctx.Done():
			return ExecResult{Error: ctx.Err().Error()}
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{module: module, args: args})
	r := f.result
	f.mu.Unlock()
	return r
}

func (f *fakeExec) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeExec) Calls() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeCall, len(f.calls))
	copy(out, f.calls)
	return out
}

type returnCall struct {
	entry  Entry
	result ExecResult
}

func baseTime() time.Time {
	return time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
}

func TestRunner_IntervalEntryFires(t *testing.T) {
	clk := newFakeClock(baseTime())
	exec := newFakeExec()

	r := NewRunner(RunnerConfig{
		PeelID: "peel-1",
		ExecFn: exec.Exec,
		Now:    clk.Now,
	})

	entries := []Entry{
		{
			Name:       "check",
			Module:     "test.ping",
			Interval:   5 * time.Minute,
			MaxRunning: 1,
			Enabled:    true,
			Source:     SourceConfig,
		},
	}
	r.Load(entries)

	// Advance past the interval.
	clk.Advance(6 * time.Minute)
	r.tick(context.Background())

	// Wait briefly for the goroutine to complete.
	time.Sleep(50 * time.Millisecond)

	if exec.CallCount() != 1 {
		t.Errorf("expected 1 call, got %d", exec.CallCount())
	}
	calls := exec.Calls()
	if calls[0].module != "test.ping" {
		t.Errorf("module = %q, want %q", calls[0].module, "test.ping")
	}
}

func TestRunner_CronEntryFires(t *testing.T) {
	// Start at 10:00 — next "0 * * * *" fires at 11:00.
	clk := newFakeClock(baseTime())
	exec := newFakeExec()

	r := NewRunner(RunnerConfig{
		PeelID: "peel-1",
		ExecFn: exec.Exec,
		Now:    clk.Now,
	})

	entries := []Entry{
		{
			Name:       "hourly",
			Module:     "state.highstate",
			Cron:       "0 * * * *",
			MaxRunning: 1,
			Enabled:    true,
			Source:     SourceConfig,
		},
	}
	r.Load(entries)

	// Advance to 10:59 — should not fire yet.
	clk.Advance(59 * time.Minute)
	r.tick(context.Background())
	time.Sleep(30 * time.Millisecond)

	if exec.CallCount() != 0 {
		t.Errorf("expected 0 calls before cron time, got %d", exec.CallCount())
	}

	// Advance to 11:01 — should fire.
	clk.Advance(2 * time.Minute)
	r.tick(context.Background())
	time.Sleep(50 * time.Millisecond)

	if exec.CallCount() != 1 {
		t.Errorf("expected 1 call after cron time, got %d", exec.CallCount())
	}
}

func TestRunner_RunOnStart(t *testing.T) {
	clk := newFakeClock(baseTime())
	exec := newFakeExec()

	r := NewRunner(RunnerConfig{
		PeelID: "peel-1",
		ExecFn: exec.Exec,
		Now:    clk.Now,
	})

	entries := []Entry{
		{
			Name:       "immediate",
			Module:     "test.ping",
			Interval:   10 * time.Minute,
			MaxRunning: 1,
			Enabled:    true,
			RunOnStart: true,
			Source:     SourceConfig,
		},
	}
	r.Load(entries)

	// First tick at t=0 should fire because RunOnStart sets nextRun=now.
	r.tick(context.Background())
	time.Sleep(50 * time.Millisecond)

	if exec.CallCount() != 1 {
		t.Errorf("expected 1 call on start, got %d", exec.CallCount())
	}
}

func TestRunner_DisabledEntryNeverFires(t *testing.T) {
	clk := newFakeClock(baseTime())
	exec := newFakeExec()

	r := NewRunner(RunnerConfig{
		PeelID: "peel-1",
		ExecFn: exec.Exec,
		Now:    clk.Now,
	})

	entries := []Entry{
		{
			Name:     "nope",
			Module:   "test.ping",
			Interval: time.Minute,
			Enabled:  false,
			Source:   SourceConfig,
		},
	}
	r.Load(entries)

	clk.Advance(10 * time.Minute)
	r.tick(context.Background())
	time.Sleep(30 * time.Millisecond)

	if exec.CallCount() != 0 {
		t.Errorf("expected 0 calls for disabled entry, got %d", exec.CallCount())
	}
}

func TestRunner_MaxRunningPreventsOverlap(t *testing.T) {
	clk := newFakeClock(baseTime())

	blockCh := make(chan struct{})
	var execCount atomic.Int32
	blockingExec := func(ctx context.Context, module string, args map[string]any) ExecResult {
		execCount.Add(1)
		select {
		case <-blockCh:
		case <-ctx.Done():
		}
		return ExecResult{Success: true}
	}

	r := NewRunner(RunnerConfig{
		PeelID: "peel-1",
		ExecFn: blockingExec,
		Now:    clk.Now,
	})

	entries := []Entry{
		{
			Name:       "limited",
			Module:     "cmd.run",
			Interval:   time.Minute,
			MaxRunning: 1,
			RunOnStart: true,
			Enabled:    true,
			Source:     SourceConfig,
		},
	}
	r.Load(entries)

	// First tick fires and blocks.
	r.tick(context.Background())
	time.Sleep(50 * time.Millisecond)

	if execCount.Load() != 1 {
		t.Fatalf("expected 1 exec, got %d", execCount.Load())
	}

	// While still running, second tick at same time should not fire again.
	r.tick(context.Background())
	time.Sleep(30 * time.Millisecond)

	if execCount.Load() != 1 {
		t.Errorf("expected still 1 exec (blocked by MaxRunning), got %d", execCount.Load())
	}

	// Unblock.
	close(blockCh)
	time.Sleep(50 * time.Millisecond)
}

func TestRunner_SplayAddsDelay(t *testing.T) {
	clk := newFakeClock(baseTime())
	exec := newFakeExec()

	r := NewRunner(RunnerConfig{
		PeelID: "peel-1",
		ExecFn: exec.Exec,
		Now:    clk.Now,
	})

	entries := []Entry{
		{
			Name:       "splayed",
			Module:     "test.ping",
			Interval:   time.Minute,
			Splay:      30 * time.Second,
			MaxRunning: 1,
			Enabled:    true,
			Source:     SourceConfig,
		},
	}
	r.Load(entries)

	// Advance exactly 1 minute — entry may or may not fire depending on splay.
	clk.Advance(time.Minute)
	r.tick(context.Background())
	time.Sleep(30 * time.Millisecond)

	// Advance the maximum splay amount — must fire by now.
	clk.Advance(31 * time.Second)
	r.tick(context.Background())
	time.Sleep(50 * time.Millisecond)

	if exec.CallCount() < 1 {
		t.Errorf("expected at least 1 call within interval+splay window, got %d", exec.CallCount())
	}
}

func TestRunner_ReloadAddsNewEntries(t *testing.T) {
	clk := newFakeClock(baseTime())
	exec := newFakeExec()

	r := NewRunner(RunnerConfig{
		PeelID: "peel-1",
		ExecFn: exec.Exec,
		Now:    clk.Now,
	})

	r.Load([]Entry{})

	// Reload adds a new entry.
	r.Reload([]Entry{
		{
			Name:       "new",
			Module:     "test.ping",
			Interval:   time.Minute,
			MaxRunning: 1,
			Enabled:    true,
			Source:     SourceConfig,
		},
	}, SourceConfig)

	names := r.Entries()
	if len(names) != 1 || names[0] != "new" {
		t.Errorf("expected [new], got %v", names)
	}
}

func TestRunner_ReloadRemovesEntries(t *testing.T) {
	clk := newFakeClock(baseTime())
	exec := newFakeExec()

	r := NewRunner(RunnerConfig{
		PeelID: "peel-1",
		ExecFn: exec.Exec,
		Now:    clk.Now,
	})

	r.Load([]Entry{
		{
			Name:       "old",
			Module:     "test.ping",
			Interval:   time.Minute,
			MaxRunning: 1,
			Enabled:    true,
			Source:     SourceConfig,
		},
	})

	// Reload with empty list removes the entry from config source.
	r.Reload([]Entry{}, SourceConfig)

	names := r.Entries()
	if len(names) != 0 {
		t.Errorf("expected 0 entries after reload, got %v", names)
	}
}

func TestRunner_ReloadPreservesStateForUnchanged(t *testing.T) {
	clk := newFakeClock(baseTime())
	exec := newFakeExec()

	r := NewRunner(RunnerConfig{
		PeelID: "peel-1",
		ExecFn: exec.Exec,
		Now:    clk.Now,
	})

	e := Entry{
		Name:       "stable",
		Module:     "test.ping",
		Interval:   5 * time.Minute,
		MaxRunning: 1,
		Enabled:    true,
		Source:     SourceConfig,
	}
	r.Load([]Entry{e})

	// Capture initial nextRun.
	r.mu.RLock()
	initialNext := r.entries["stable"].nextRun
	r.mu.RUnlock()

	// Reload with identical entry — nextRun should be preserved.
	e.Source = SourceConfig
	r.Reload([]Entry{e}, SourceConfig)

	r.mu.RLock()
	reloadedNext := r.entries["stable"].nextRun
	r.mu.RUnlock()

	if !initialNext.Equal(reloadedNext) {
		t.Errorf("nextRun changed for unchanged entry: %v -> %v", initialNext, reloadedNext)
	}
}

func TestRunner_ReloadKeepsOtherSourceEntries(t *testing.T) {
	clk := newFakeClock(baseTime())
	exec := newFakeExec()

	r := NewRunner(RunnerConfig{
		PeelID: "peel-1",
		ExecFn: exec.Exec,
		Now:    clk.Now,
	})

	// Load config entry.
	r.Load([]Entry{
		{
			Name:       "config-entry",
			Module:     "test.ping",
			Interval:   time.Minute,
			MaxRunning: 1,
			Enabled:    true,
			Source:     SourceConfig,
		},
	})

	// Reload settings source — should not remove config entry.
	r.Reload([]Entry{
		{
			Name:       "settings-entry",
			Module:     "cmd.run",
			Interval:   time.Minute,
			MaxRunning: 1,
			Enabled:    true,
			Source:     SourceSettings,
		},
	}, SourceSettings)

	names := r.Entries()
	if len(names) != 2 {
		t.Errorf("expected 2 entries (both sources), got %v", names)
	}
}

func TestRunner_Reload_SettingsRemovalRestoresConfigFallback(t *testing.T) {
	clk := newFakeClock(baseTime())
	exec := newFakeExec()

	r := NewRunner(RunnerConfig{
		PeelID: "peel-1",
		ExecFn: exec.Exec,
		Now:    clk.Now,
	})

	// Config baseline entry.
	r.Load([]Entry{
		{
			Name:       "backup",
			Module:     "cmd.run",
			Args:       map[string]any{"command": "backup-full"},
			Interval:   time.Hour,
			MaxRunning: 1,
			Enabled:    true,
			Source:     SourceConfig,
		},
	})

	// Settings override with same name.
	r.Reload([]Entry{
		{
			Name:       "backup",
			Module:     "cmd.run",
			Args:       map[string]any{"command": "backup-incremental"},
			Interval:   10 * time.Minute,
			MaxRunning: 1,
			Enabled:    true,
			Source:     SourceSettings,
		},
	}, SourceSettings)

	r.mu.RLock()
	if got := r.entries["backup"].entry.Source; got != SourceSettings {
		r.mu.RUnlock()
		t.Fatalf("expected settings override active, got source %q", got)
	}
	r.mu.RUnlock()

	// Remove settings override.
	r.Reload([]Entry{}, SourceSettings)

	r.mu.RLock()
	es, ok := r.entries["backup"]
	r.mu.RUnlock()
	if !ok {
		t.Fatal("expected config fallback entry to remain after settings removal")
	}
	if es.entry.Source != SourceConfig {
		t.Fatalf("expected fallback source %q, got %q", SourceConfig, es.entry.Source)
	}
	if es.entry.Interval != time.Hour {
		t.Fatalf("expected fallback interval %v, got %v", time.Hour, es.entry.Interval)
	}
}

func TestRunner_Reload_ConfigDoesNotOverrideSettings(t *testing.T) {
	clk := newFakeClock(baseTime())
	exec := newFakeExec()

	r := NewRunner(RunnerConfig{
		PeelID: "peel-1",
		ExecFn: exec.Exec,
		Now:    clk.Now,
	})

	// Config entry.
	r.Load([]Entry{
		{
			Name:       "backup",
			Module:     "cmd.run",
			Interval:   time.Hour,
			MaxRunning: 1,
			Enabled:    true,
			Source:     SourceConfig,
		},
	})

	// Settings override.
	r.Reload([]Entry{
		{
			Name:       "backup",
			Module:     "cmd.run",
			Interval:   10 * time.Minute,
			MaxRunning: 1,
			Enabled:    true,
			Source:     SourceSettings,
		},
	}, SourceSettings)

	// Re-load config with different value; settings should still win.
	r.Reload([]Entry{
		{
			Name:       "backup",
			Module:     "cmd.run",
			Interval:   2 * time.Hour,
			MaxRunning: 1,
			Enabled:    true,
			Source:     SourceConfig,
		},
	}, SourceConfig)

	r.mu.RLock()
	es := r.entries["backup"]
	r.mu.RUnlock()
	if es.entry.Source != SourceSettings {
		t.Fatalf("expected effective source %q, got %q", SourceSettings, es.entry.Source)
	}
	if es.entry.Interval != 10*time.Minute {
		t.Fatalf("expected effective interval %v, got %v", 10*time.Minute, es.entry.Interval)
	}
}

func TestRunner_ReturnJobCallsReturnFn(t *testing.T) {
	clk := newFakeClock(baseTime())
	exec := newFakeExec()
	exec.result = ExecResult{Success: true, ResultData: "test-data"}

	var returnCalls []returnCall
	var returnMu sync.Mutex
	returnFn := func(entry Entry, result ExecResult) {
		returnMu.Lock()
		returnCalls = append(returnCalls, returnCall{entry: entry, result: result})
		returnMu.Unlock()
	}

	r := NewRunner(RunnerConfig{
		PeelID:   "peel-1",
		ExecFn:   exec.Exec,
		ReturnFn: returnFn,
		Now:      clk.Now,
	})

	r.Load([]Entry{
		{
			Name:       "with-return",
			Module:     "cmd.run",
			Args:       map[string]any{"command": "echo hi"},
			Interval:   time.Minute,
			MaxRunning: 1,
			RunOnStart: true,
			ReturnJob:  true,
			Enabled:    true,
			Source:     SourceConfig,
		},
		{
			Name:       "without-return",
			Module:     "test.ping",
			Interval:   time.Minute,
			MaxRunning: 1,
			RunOnStart: true,
			ReturnJob:  false,
			Enabled:    true,
			Source:     SourceConfig,
		},
	})

	r.tick(context.Background())
	time.Sleep(50 * time.Millisecond)

	// Both entries should have fired.
	if exec.CallCount() != 2 {
		t.Fatalf("expected 2 exec calls, got %d", exec.CallCount())
	}

	// Only the return_job entry should have called ReturnFn.
	returnMu.Lock()
	defer returnMu.Unlock()
	if len(returnCalls) != 1 {
		t.Fatalf("expected 1 return call, got %d", len(returnCalls))
	}
	if returnCalls[0].entry.Name != "with-return" {
		t.Errorf("return call name = %q, want %q", returnCalls[0].entry.Name, "with-return")
	}
	if !returnCalls[0].result.Success {
		t.Errorf("return call result.Success = false, want true")
	}
	if returnCalls[0].result.ResultData != "test-data" {
		t.Errorf("return call result.ResultData = %v, want %q", returnCalls[0].result.ResultData, "test-data")
	}
}

func TestRunner_ReturnJobIncludesError(t *testing.T) {
	clk := newFakeClock(baseTime())
	exec := newFakeExec()
	exec.result = ExecResult{Success: false, Error: "module failed"}

	var returnCalls []returnCall
	var returnMu sync.Mutex
	returnFn := func(entry Entry, result ExecResult) {
		returnMu.Lock()
		returnCalls = append(returnCalls, returnCall{entry: entry, result: result})
		returnMu.Unlock()
	}

	r := NewRunner(RunnerConfig{
		PeelID:   "peel-1",
		ExecFn:   exec.Exec,
		ReturnFn: returnFn,
		Now:      clk.Now,
	})

	r.Load([]Entry{
		{
			Name:       "fail-entry",
			Module:     "cmd.run",
			Interval:   time.Minute,
			MaxRunning: 1,
			RunOnStart: true,
			ReturnJob:  true,
			Enabled:    true,
			Source:     SourceConfig,
		},
	})

	r.tick(context.Background())
	time.Sleep(50 * time.Millisecond)

	returnMu.Lock()
	defer returnMu.Unlock()
	if len(returnCalls) != 1 {
		t.Fatalf("expected 1 return call, got %d", len(returnCalls))
	}
	if returnCalls[0].result.Success {
		t.Error("expected result.Success = false")
	}
	if returnCalls[0].result.Error != "module failed" {
		t.Errorf("result.Error = %q, want %q", returnCalls[0].result.Error, "module failed")
	}
}

func TestRunner_ReturnJobNilReturnFnIsNoOp(t *testing.T) {
	clk := newFakeClock(baseTime())
	exec := newFakeExec()

	// ReturnFn is nil — should not panic.
	r := NewRunner(RunnerConfig{
		PeelID: "peel-1",
		ExecFn: exec.Exec,
		Now:    clk.Now,
	})

	r.Load([]Entry{
		{
			Name:       "return-no-fn",
			Module:     "test.ping",
			Interval:   time.Minute,
			MaxRunning: 1,
			RunOnStart: true,
			ReturnJob:  true,
			Enabled:    true,
			Source:     SourceConfig,
		},
	})

	r.tick(context.Background())
	time.Sleep(50 * time.Millisecond)

	if exec.CallCount() != 1 {
		t.Errorf("expected 1 exec call, got %d", exec.CallCount())
	}
}

func TestRunner_ExecResultCarriesData(t *testing.T) {
	clk := newFakeClock(baseTime())

	type payload struct{ Value int }
	exec := newFakeExec()
	exec.result = ExecResult{Success: true, ResultData: payload{Value: 42}}

	var returnCalls []returnCall
	var returnMu sync.Mutex
	returnFn := func(entry Entry, result ExecResult) {
		returnMu.Lock()
		returnCalls = append(returnCalls, returnCall{entry: entry, result: result})
		returnMu.Unlock()
	}

	r := NewRunner(RunnerConfig{
		PeelID:   "peel-1",
		ExecFn:   exec.Exec,
		ReturnFn: returnFn,
		Now:      clk.Now,
	})

	r.Load([]Entry{
		{
			Name:       "data-entry",
			Module:     "cmd.run",
			Interval:   time.Minute,
			MaxRunning: 1,
			RunOnStart: true,
			ReturnJob:  true,
			Enabled:    true,
			Source:     SourceConfig,
		},
	})

	r.tick(context.Background())
	time.Sleep(50 * time.Millisecond)

	returnMu.Lock()
	defer returnMu.Unlock()
	if len(returnCalls) != 1 {
		t.Fatalf("expected 1 return call, got %d", len(returnCalls))
	}
	p, ok := returnCalls[0].result.ResultData.(payload)
	if !ok {
		t.Fatalf("ResultData type = %T, want payload", returnCalls[0].result.ResultData)
	}
	if p.Value != 42 {
		t.Errorf("ResultData.Value = %d, want 42", p.Value)
	}
}

func TestRunner_StopWaitsForInFlight(t *testing.T) {
	clk := newFakeClock(baseTime())

	started := make(chan struct{})
	done := make(chan struct{})
	blockingExec := func(ctx context.Context, module string, args map[string]any) ExecResult {
		close(started)
		<-ctx.Done()
		close(done)
		return ExecResult{Success: true}
	}

	r := NewRunner(RunnerConfig{
		PeelID: "peel-1",
		ExecFn: blockingExec,
		Now:    clk.Now,
	})

	r.Load([]Entry{
		{
			Name:       "long-running",
			Module:     "cmd.run",
			Interval:   time.Minute,
			MaxRunning: 1,
			RunOnStart: true,
			Enabled:    true,
			Source:     SourceConfig,
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)

	// Wait for the exec to start.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("exec never started")
	}

	// Cancel and stop.
	cancel()
	r.Stop()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not wait for in-flight exec")
	}
}
