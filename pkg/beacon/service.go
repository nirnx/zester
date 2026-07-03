package beacon

import (
	"context"
	"log/slog"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/event"
	"github.com/ptorbus/zester/pkg/exec"
)

// serviceTag is the slash tag of every service beacon event:
// "beacon/<peelID>/service" — exactly what event.ParseSubject derives from
// bus.BeaconSubject(peelID, ServiceBeaconName) (pinned by
// TestServiceSubjectTagRoundTrip).
func serviceTag(peelID string) string {
	return bus.SubjectBeacon + "/" + peelID + "/" + ServiceBeaconName
}

// serviceBeacon polls configured services' running state via the
// exec.ServiceExec provider and turns transitions into events. It keeps a
// per-service baseline of the last observed state; baselines are owned by
// the Manager's tick path (see the Manager concurrency note).
type serviceBeacon struct {
	peelID string
	svc    exec.ServiceExec
	logger *slog.Logger
	now    func() time.Time

	// prev holds the last observed running state per service.
	prev map[string]bool
}

func newServiceBeacon(peelID string, svc exec.ServiceExec, logger *slog.Logger, now func() time.Time) *serviceBeacon {
	return &serviceBeacon{
		peelID: peelID,
		svc:    svc,
		logger: logger,
		now:    now,
		prev:   make(map[string]bool),
	}
}

// poll observes each configured service and returns the events to emit this
// cycle. With OnChangeOnly (the default) the first observation of a service
// establishes its baseline WITHOUT emitting; later polls emit only on
// running-state transitions. With OnChangeOnly false every poll emits every
// service's state ("previous" equals "running" on the very first
// observation, since no earlier state exists). A provider error keeps the
// old baseline and emits nothing for that service (Debug-logged — polls run
// every few seconds, Warn would spam).
//
// Baselines of services no longer in cfg are pruned, so a hot-swapped
// service set re-baselines cleanly if re-added later.
func (b *serviceBeacon) poll(ctx context.Context, cfg ServiceConfig) []event.Event {
	var evs []event.Event
	configured := make(map[string]struct{}, len(cfg.Services))
	for _, name := range cfg.Services {
		configured[name] = struct{}{}

		running, err := b.svc.IsRunning(ctx, name)
		if err != nil {
			b.logger.Debug("beacon: service state poll failed", "service", name, "error", err)
			continue
		}

		prevRunning, seen := b.prev[name]
		b.prev[name] = running

		if cfg.OnChangeOnly && (!seen || prevRunning == running) {
			continue
		}
		if !seen {
			prevRunning = running
		}

		ev := event.NewEvent(serviceTag(b.peelID), map[string]any{
			"service":  name,
			"running":  running,
			"previous": prevRunning,
		})
		ev.TS = b.now().UTC()
		evs = append(evs, ev)
	}

	for name := range b.prev {
		if _, ok := configured[name]; !ok {
			delete(b.prev, name)
		}
	}
	return evs
}
