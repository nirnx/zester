package beacon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/event"
	"github.com/ptorbus/zester/pkg/exec"
)

// DefaultBufferSize bounds the offline event buffer: events whose publish
// fails (NATS down) are kept in a FIFO of this size — oldest dropped first at
// Debug — and drained, in order, on the next tick whose publish succeeds.
const DefaultBufferSize = 256

// ManagerConfig configures a beacon Manager.
type ManagerConfig struct {
	// PeelID is this peel's ID — the event origin and a tag component.
	PeelID string

	// Publish sends one event on a subject. Required in production: the peel
	// wires it to msgpack-encode + core-NATS publish (the events JetStream
	// stream captures it server-side, the ScheduledResult pattern). A publish
	// error buffers the event for a later drain.
	Publish func(subject string, ev event.Event) error

	// Service is the service-state provider. nil disables the service beacon
	// with a single Warn (logged once services are actually configured).
	Service exec.ServiceExec

	// BusyFn reports whether the peel's exec worker is running a mutating
	// execution; polls are skipped while true (disable_during_state_run) so
	// a reaction-triggered state run does not re-trip the beacon that caused
	// it. Buffer draining still runs on busy ticks. nil means never busy.
	BusyFn func() bool

	// OnEvent is invoked once per GENERATED event (before publish, so
	// offline-buffered events count too) with the beacon name — the hook for
	// the pre-registered zester_peel_beacon_events_total{beacon} metric.
	// nil is a no-op.
	OnEvent func(beacon string)

	// Logger defaults to slog.Default().
	Logger *slog.Logger

	// Now is the clock used to stamp event timestamps (tests inject a fake);
	// defaults to time.Now.
	Now func() time.Time
}

// outbound is one buffered (subject, event) pair awaiting publish.
type outbound struct {
	subject string
	ev      event.Event
}

// Manager runs the beacon poll loop: every interval it polls each configured
// beacon (v1: only service), emits the resulting events, and drains the
// offline buffer. Config is hot-swappable via UpdateConfig and is picked up
// on the next tick.
//
// Concurrency: cfg is guarded by mu (UpdateConfig / CurrentConfig can be
// called from any goroutine); buf, warnedNilService, and the per-beacon
// baselines are owned exclusively by the tick path — Run's single goroutine
// in production, the test's goroutine in unit tests.
type Manager struct {
	peelID  string
	publish func(subject string, ev event.Event) error
	busy    func() bool
	onEvent func(beacon string)
	logger  *slog.Logger
	now     func() time.Time

	// svc is the service beacon; nil when no ServiceExec provider exists on
	// this platform (the beacon is then disabled with one Warn).
	svc *serviceBeacon

	mu  sync.RWMutex
	cfg Config

	// bufCap is DefaultBufferSize in production; tests shrink it.
	bufCap           int
	buf              []outbound
	warnedNilService bool
}

// NewManager builds a Manager. Zero-value ManagerConfig fields get safe
// defaults (see the field docs); a nil Publish returns an error on every
// attempt, so events just accumulate in the bounded buffer.
func NewManager(cfg ManagerConfig) *Manager {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	busy := cfg.BusyFn
	if busy == nil {
		busy = func() bool { return false }
	}
	onEvent := cfg.OnEvent
	if onEvent == nil {
		onEvent = func(string) {}
	}
	publish := cfg.Publish
	if publish == nil {
		publish = func(string, event.Event) error {
			return fmt.Errorf("beacon: no publish function configured")
		}
	}

	m := &Manager{
		peelID:  cfg.PeelID,
		publish: publish,
		busy:    busy,
		onEvent: onEvent,
		logger:  logger,
		now:     now,
		bufCap:  DefaultBufferSize,
	}
	if cfg.Service != nil {
		m.svc = newServiceBeacon(cfg.PeelID, cfg.Service, logger, now)
	}
	return m
}

// UpdateConfig hot-swaps the beacon configuration; the next tick picks it up.
// An empty Config (Service nil) disables polling without stopping the loop,
// so a later settings change can re-enable it.
func (m *Manager) UpdateConfig(cfg Config) {
	m.mu.Lock()
	m.cfg = cfg
	m.mu.Unlock()

	if cfg.Service != nil {
		m.logger.Debug("beacon config updated",
			"services", len(cfg.Service.Services),
			"interval", cfg.Service.Interval,
			"onchangeonly", cfg.Service.OnChangeOnly)
	} else {
		m.logger.Debug("beacon config updated", "services", 0)
	}
}

// CurrentConfig returns a copy of the active configuration (test/observability
// accessor; the Service pointer and Services slice are copied so callers can
// never alias the Manager's state).
func (m *Manager) CurrentConfig() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out Config
	if m.cfg.Service != nil {
		sc := *m.cfg.Service
		sc.Services = append([]string(nil), m.cfg.Service.Services...)
		out.Service = &sc
	}
	return out
}

// Run drives the poll loop until ctx is cancelled. The wait duration is
// re-read every cycle, so an UpdateConfig interval change takes effect on the
// next tick.
func (m *Manager) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(m.interval()):
			m.tick(ctx)
		}
	}
}

// interval returns the current poll cadence: the service beacon's configured
// interval, or DefaultInterval while unconfigured (the loop keeps ticking so
// a hot-swapped config is noticed).
func (m *Manager) interval() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cfg.Service != nil && m.cfg.Service.Interval > 0 {
		return m.cfg.Service.Interval
	}
	return DefaultInterval
}

// tick runs one poll cycle: unless the exec worker is busy, poll every
// configured beacon and enqueue its events; then attempt to drain the buffer
// (drain runs even on busy ticks — publishing buffered events is harmless
// and keeps the offline buffer from lingering a full state run).
func (m *Manager) tick(ctx context.Context) {
	m.mu.RLock()
	svcCfg := m.cfg.Service
	m.mu.RUnlock()

	if svcCfg != nil && len(svcCfg.Services) > 0 {
		switch {
		case m.busy():
			m.logger.Debug("beacon: poll skipped, exec worker busy")
		case m.svc == nil:
			if !m.warnedNilService {
				m.warnedNilService = true
				m.logger.Warn("service beacon disabled: no service provider detected on this platform")
			}
		default:
			for _, ev := range m.svc.poll(ctx, *svcCfg) {
				m.onEvent(ServiceBeaconName)
				m.enqueue(bus.BeaconSubject(m.peelID, ServiceBeaconName), ev)
			}
		}
	}

	m.flush()
}

// enqueue appends an event to the bounded offline buffer, dropping the
// oldest entry at Debug when full.
func (m *Manager) enqueue(subject string, ev event.Event) {
	if len(m.buf) >= m.bufCap {
		dropped := m.buf[0]
		m.buf = m.buf[1:]
		m.logger.Debug("beacon: offline buffer full, dropping oldest event",
			"tag", dropped.ev.Tag, "event_id", dropped.ev.ID, "cap", m.bufCap)
	}
	m.buf = append(m.buf, outbound{subject: subject, ev: ev})
}

// flush publishes buffered events in FIFO order, stopping at the first
// failure (the remainder stays buffered for the next tick).
func (m *Manager) flush() {
	for len(m.buf) > 0 {
		ob := m.buf[0]
		if err := m.publish(ob.subject, ob.ev); err != nil {
			m.logger.Debug("beacon: publish failed, keeping events buffered",
				"subject", ob.subject, "buffered", len(m.buf), "error", err)
			return
		}
		m.buf = m.buf[1:]
	}
	m.buf = nil
}
