package schedule

import (
	"fmt"
	"time"
)

// EntrySource indicates where a schedule entry was defined.
type EntrySource string

const (
	SourceConfig   EntrySource = "config"
	SourceSettings EntrySource = "settings"
)

// Entry is a fully-parsed, validated schedule entry ready for the Runner.
type Entry struct {
	Name       string
	Module     string // e.g. "cmd.run", "state.highstate"
	Args       map[string]any
	Interval   time.Duration // mutually exclusive with Cron
	Cron       string        // standard 5-field cron expression
	Splay      time.Duration // random delay [0, splay) per fire
	MaxRunning int           // default 1
	RunOnStart bool          // fire immediately on scheduler start
	ReturnJob  bool          // create a real Job for tracking
	Enabled    bool          // default true
	Source     EntrySource   // "config" or "settings"
}

// Validate checks that the entry is well-formed.
func (e *Entry) Validate() error {
	if e.Name == "" {
		return fmt.Errorf("schedule: entry name is required")
	}
	if e.Module == "" {
		return fmt.Errorf("schedule: entry %q: module is required", e.Name)
	}
	if e.Interval == 0 && e.Cron == "" {
		return fmt.Errorf("schedule: entry %q: interval or cron is required", e.Name)
	}
	if e.Interval != 0 && e.Cron != "" {
		return fmt.Errorf("schedule: entry %q: interval and cron are mutually exclusive", e.Name)
	}
	if e.Interval < 0 {
		return fmt.Errorf("schedule: entry %q: interval must be positive", e.Name)
	}
	if e.Cron != "" {
		if _, err := ParseCron(e.Cron); err != nil {
			return fmt.Errorf("schedule: entry %q: %w", e.Name, err)
		}
	}
	if e.MaxRunning < 0 {
		return fmt.Errorf("schedule: entry %q: maxrunning must be non-negative", e.Name)
	}
	return nil
}
