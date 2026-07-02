package schedule

import (
	"fmt"
	"time"
)

// RawEntry is the YAML-unmarshalled schedule entry from config or settings.
type RawEntry struct {
	Module     string         `yaml:"module"`
	Args       map[string]any `yaml:"args,omitempty"`
	Interval   string         `yaml:"interval,omitempty"` // parsed as time.Duration
	Cron       string         `yaml:"cron,omitempty"`
	Splay      string         `yaml:"splay,omitempty"` // parsed as time.Duration
	MaxRunning int            `yaml:"maxrunning,omitempty"`
	RunOnStart bool           `yaml:"run_on_start,omitempty"`
	ReturnJob  bool           `yaml:"return_job,omitempty"`
	Enabled    *bool          `yaml:"enabled,omitempty"` // defaults to true
}

// Parse converts a map of raw entries (from YAML config or settings) into validated Entry slices.
// Invalid entries are skipped and returned in the error slice.
func Parse(raw map[string]RawEntry, source EntrySource) ([]Entry, []error) {
	var entries []Entry
	var errs []error

	for name, r := range raw {
		e := Entry{
			Name:       name,
			Module:     r.Module,
			Args:       r.Args,
			Cron:       r.Cron,
			MaxRunning: r.MaxRunning,
			RunOnStart: r.RunOnStart,
			ReturnJob:  r.ReturnJob,
			Enabled:    true,
			Source:     source,
		}

		if r.Enabled != nil {
			e.Enabled = *r.Enabled
		}

		if r.Interval != "" {
			d, err := time.ParseDuration(r.Interval)
			if err != nil {
				errs = append(errs, fmt.Errorf("schedule: entry %q: parse interval: %w", name, err))
				continue
			}
			e.Interval = d
		}

		if r.Splay != "" {
			d, err := time.ParseDuration(r.Splay)
			if err != nil {
				errs = append(errs, fmt.Errorf("schedule: entry %q: parse splay: %w", name, err))
				continue
			}
			e.Splay = d
		}

		if e.MaxRunning == 0 {
			e.MaxRunning = 1
		}

		if !e.Enabled {
			continue
		}

		if err := e.Validate(); err != nil {
			errs = append(errs, err)
			continue
		}

		entries = append(entries, e)
	}

	return entries, errs
}

// ParseSettings extracts schedule entries from a resolved settings map.
// It looks for a top-level "schedule" key containing a map of entry configs.
func ParseSettings(settings map[string]any, source EntrySource) ([]Entry, []error) {
	schedRaw, ok := settings["schedule"]
	if !ok {
		return nil, nil
	}

	schedMap, ok := schedRaw.(map[string]any)
	if !ok {
		return nil, []error{fmt.Errorf("schedule: settings 'schedule' key must be a map")}
	}

	// Convert the generic map into RawEntry structs by going through each entry.
	raw := make(map[string]RawEntry)
	for name, v := range schedMap {
		entryMap, ok := v.(map[string]any)
		if !ok {
			continue
		}
		r := RawEntry{}
		if m, ok := entryMap["module"].(string); ok {
			r.Module = m
		}
		if a, ok := entryMap["args"].(map[string]any); ok {
			r.Args = a
		}
		if i, ok := entryMap["interval"].(string); ok {
			r.Interval = i
		}
		if c, ok := entryMap["cron"].(string); ok {
			r.Cron = c
		}
		if s, ok := entryMap["splay"].(string); ok {
			r.Splay = s
		}
		if m, ok := entryMap["maxrunning"].(int); ok {
			r.MaxRunning = m
		}
		if b, ok := entryMap["run_on_start"].(bool); ok {
			r.RunOnStart = b
		}
		if b, ok := entryMap["return_job"].(bool); ok {
			r.ReturnJob = b
		}
		if b, ok := entryMap["enabled"].(bool); ok {
			r.Enabled = &b
		}
		raw[name] = r
	}

	return Parse(raw, source)
}
