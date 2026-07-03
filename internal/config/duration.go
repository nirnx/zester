package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration whose YAML form matches the flag form of the
// same knob: any value time.ParseDuration accepts — unit-suffixed strings
// ("30s", "1h") and the bare scalar 0 (the documented "0 disables" form).
// Plain time.Duration fields reject bare YAML integers outright ("yaml:
// cannot unmarshal !!int 0 into time.Duration"), which made the documented
// max_event_age: 0 / default_throttle: 0 forms brick the daemon at boot.
// Non-zero bare numbers stay errors: "30" is ambiguous where "30s" is not.
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler with time.ParseDuration
// semantics.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("config: invalid duration: expected a scalar value, got %s", value.Tag)
	}
	parsed, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("config: invalid duration %q (use a unit-suffixed value like \"30s\", or 0 to disable)", value.Value)
	}
	*d = Duration(parsed)
	return nil
}
