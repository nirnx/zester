// Package beacon implements the peel-side beacon engine: periodic local
// observations published as events on the zester.event.<peelID>.beacon.>
// namespace, feeding the master-side reactor.
//
// v1 ships exactly ONE beacon — "service" — which polls configured services'
// running state through the exec.ServiceExec provider and emits transition
// events (Salt's service beacon shape). The full beacon set (diskusage,
// memusage, load, filewatch, ...) is Phase 2.
//
// Configuration arrives through the settings pipeline (hot-reloaded on
// settings changes, warm-started from the on-disk snapshot) under the
// "beacons" key, Salt-compat shape:
//
//	beacons:
//	  service:
//	    services:
//	      nginx: {}
//	    interval: 10        # seconds (or a duration string like "10s")
//	    onchangeonly: true  # default true: emit only on state TRANSITIONS
package beacon

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

const (
	// SettingsKey is the top-level settings key holding beacon configuration.
	SettingsKey = "beacons"

	// ServiceBeaconName is the name of v1's only beacon; it is both the
	// beacon subject token (bus.BeaconSubject(peelID, ServiceBeaconName))
	// and the metric label value.
	ServiceBeaconName = "service"

	// DefaultInterval is the poll cadence when no interval is configured.
	DefaultInterval = 10 * time.Second
)

// Config is the parsed beacon configuration. The zero value disables all
// beacons.
type Config struct {
	// Service is the service beacon's configuration; nil when unconfigured.
	Service *ServiceConfig
}

// ServiceConfig configures the service beacon.
type ServiceConfig struct {
	// Services is the sorted list of service names to poll.
	Services []string

	// Interval is the poll cadence (default DefaultInterval).
	Interval time.Duration

	// OnChangeOnly emits only on running-state transitions (default true);
	// the first poll of a service establishes its baseline without emitting.
	// When false, every poll emits every configured service's state.
	OnChangeOnly bool
}

// ParseConfig extracts beacon configuration from a resolved settings map
// (the SettingsKey key). Parsing is best-effort, mirroring
// schedule.ParseSettings: the returned Config contains whatever parsed
// cleanly and the (possibly errors.Join-ed) error describes what did not —
// callers apply the Config and Warn-log the error. An absent or nil
// "beacons" key is a valid empty configuration, not an error.
func ParseConfig(settings map[string]any) (Config, error) {
	var cfg Config
	if settings == nil {
		return cfg, nil
	}
	raw, ok := settings[SettingsKey]
	if !ok || raw == nil {
		return cfg, nil
	}
	beacons, ok := raw.(map[string]any)
	if !ok {
		return cfg, fmt.Errorf("beacon: settings key %q must be a map, got %T", SettingsKey, raw)
	}

	var errs []error
	for name, v := range beacons {
		if name != ServiceBeaconName {
			errs = append(errs, fmt.Errorf("beacon: unknown beacon %q (v1 supports only %q)", name, ServiceBeaconName))
			continue
		}
		sc, err := parseServiceConfig(v)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		cfg.Service = sc
	}
	return cfg, errors.Join(errs...)
}

// parseServiceConfig parses the service beacon section. Any field error
// rejects the WHOLE section (nil + error): a half-applied service beacon
// (e.g. a mistyped onchangeonly silently defaulting to true/false) could
// either flood events or suppress wanted ones.
func parseServiceConfig(v any) (*ServiceConfig, error) {
	sc := &ServiceConfig{Interval: DefaultInterval, OnChangeOnly: true}
	if v == nil {
		return sc, nil // "service:" with no body — defaults, no services
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("beacon: service beacon config must be a map, got %T", v)
	}

	var errs []error
	if raw, ok := m["services"]; ok && raw != nil {
		svcs, err := parseServiceNames(raw)
		if err != nil {
			errs = append(errs, err)
		} else {
			sc.Services = svcs
		}
	}
	if raw, ok := m["interval"]; ok && raw != nil {
		iv, err := parseInterval(raw)
		if err != nil {
			errs = append(errs, err)
		} else {
			sc.Interval = iv
		}
	}
	if raw, ok := m["onchangeonly"]; ok && raw != nil {
		b, ok := raw.(bool)
		if !ok {
			errs = append(errs, fmt.Errorf("beacon: service onchangeonly must be a bool, got %T", raw))
		} else {
			sc.OnChangeOnly = b
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return sc, nil
}

// parseServiceNames accepts the Salt-compat map form
// (services: {nginx: {}} — per-service options are ignored in v1) and, for
// convenience, a plain list of names (services: [nginx, redis]). Names are
// returned sorted for deterministic polling order.
func parseServiceNames(v any) ([]string, error) {
	var names []string
	switch t := v.(type) {
	case map[string]any:
		for name := range t {
			names = append(names, name)
		}
	case []any:
		for _, e := range t {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("beacon: service list entries must be strings, got %T", e)
			}
			names = append(names, s)
		}
	case []string:
		names = append(names, t...)
	default:
		return nil, fmt.Errorf("beacon: services must be a map of name -> options or a list of names, got %T", v)
	}
	for _, n := range names {
		if n == "" {
			return nil, fmt.Errorf("beacon: empty service name")
		}
	}
	sort.Strings(names)
	return names, nil
}

// parseInterval accepts Salt-compat bare numbers (seconds) in every integer
// and float width the YAML and msgpack (settings-snapshot round-trip)
// decoders produce, plus Go duration strings ("30s").
func parseInterval(v any) (time.Duration, error) {
	var secs float64
	switch t := v.(type) {
	case int:
		secs = float64(t)
	case int8:
		secs = float64(t)
	case int16:
		secs = float64(t)
	case int32:
		secs = float64(t)
	case int64:
		secs = float64(t)
	case uint:
		secs = float64(t)
	case uint8:
		secs = float64(t)
	case uint16:
		secs = float64(t)
	case uint32:
		secs = float64(t)
	case uint64:
		secs = float64(t)
	case float32:
		secs = float64(t)
	case float64:
		secs = t
	case string:
		d, err := time.ParseDuration(t)
		if err != nil {
			return 0, fmt.Errorf("beacon: parse interval %q: %w", t, err)
		}
		if d <= 0 {
			return 0, fmt.Errorf("beacon: interval must be positive, got %s", d)
		}
		return d, nil
	default:
		return 0, fmt.Errorf("beacon: interval must be a number of seconds or a duration string, got %T", v)
	}
	if secs <= 0 {
		return 0, fmt.Errorf("beacon: interval must be positive, got %v", v)
	}
	return time.Duration(secs * float64(time.Second)), nil
}
