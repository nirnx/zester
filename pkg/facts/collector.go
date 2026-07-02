// Package facts implements the Zester facts collection system.
// Facts are system information about a peel (Salt grains equivalent),
// collected by pluggable collectors and published to NATS KV for targeting.
package facts

import (
	"context"
	"time"
)

// Collector gathers a named set of system facts.
// Each collector is responsible for one domain (OS, network, CPU, etc.).
type Collector interface {
	// Name returns the fact namespace for this collector (e.g. "os", "network").
	Name() string

	// Collect gathers facts and returns them as a flat map.
	// Keys should be dot-free; nesting is handled by the collector name.
	// For example, the "os" collector returns {"name": "linux", "arch": "amd64"}.
	Collect(ctx context.Context) (map[string]any, error)

	// Interval returns how often to re-collect. Zero means collect once at startup.
	Interval() time.Duration
}

// RootMerger is an optional interface that collectors can implement
// to indicate their results should be merged at the top level of the
// facts map rather than nested under their Name(). This is used by
// the Custom collector so that /etc/zester/facts entries appear as
// top-level facts (like Salt's /etc/salt/grains).
type RootMerger interface {
	MergeAtRoot() bool
}

// Facts is the aggregated fact map for a single peel.
// Top-level keys are collector names, values are collector output maps.
// Example: {"os": {"name": "linux"}, "network": {"hostname": "web-01"}}
type Facts map[string]any

// Get retrieves a nested fact value using dot notation.
// Example: Get("os.name") returns the value at facts["os"]["name"].
func (f Facts) Get(key string) (any, bool) {
	return getNestedValue(f, key)
}

// getNestedValue walks a map[string]any tree using dot-separated keys.
func getNestedValue(m map[string]any, key string) (any, bool) {
	parts := splitDotPath(key)
	if len(parts) == 0 {
		return nil, false
	}

	current := any(m)
	for _, part := range parts {
		cm, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = cm[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// splitDotPath splits a dot-separated key path into parts.
func splitDotPath(key string) []string {
	if key == "" {
		return nil
	}
	var parts []string
	start := 0
	for i := 0; i < len(key); i++ {
		if key[i] == '.' {
			if i > start {
				parts = append(parts, key[start:i])
			}
			start = i + 1
		}
	}
	if start < len(key) {
		parts = append(parts, key[start:])
	}
	return parts
}

// flattenFacts converts nested Facts into dot-separated key-value pairs.
// Example: {"os": {"name": "linux"}} -> {"os.name": "linux"}
func flattenFacts(m map[string]any, prefix string) map[string]string {
	result := make(map[string]string)
	for k, v := range m {
		fullKey := k
		if prefix != "" {
			fullKey = prefix + "." + k
		}
		switch val := v.(type) {
		case map[string]any:
			for fk, fv := range flattenFacts(val, fullKey) {
				result[fk] = fv
			}
		default:
			result[fullKey] = toString(val)
		}
	}
	return result
}

// toString converts a value to its string representation for indexing.
func toString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case bool:
		if val {
			return "true"
		}
		return "false"
	case int:
		return intToStr(int64(val))
	case int8:
		return intToStr(int64(val))
	case int16:
		return intToStr(int64(val))
	case int32:
		return intToStr(int64(val))
	case int64:
		return intToStr(val)
	case uint:
		return uintToStr(uint64(val))
	case uint8:
		return uintToStr(uint64(val))
	case uint16:
		return uintToStr(uint64(val))
	case uint32:
		return uintToStr(uint64(val))
	case uint64:
		return uintToStr(val)
	case float32:
		return floatToStr(float64(val))
	case float64:
		return floatToStr(val)
	default:
		return ""
	}
}

func intToStr(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := false
	if v < 0 {
		neg = true
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func uintToStr(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func floatToStr(v float64) string {
	// Simple integer check for clean output.
	if v == float64(int64(v)) {
		return intToStr(int64(v))
	}
	// Fall back to a reasonable fixed precision.
	// We avoid importing strconv/fmt in the hot path.
	return formatFloat(v)
}

func formatFloat(f float64) string {
	// Use a simple approach: integer part + "." + 2 decimal digits.
	neg := false
	if f < 0 {
		neg = true
		f = -f
	}
	intPart := int64(f)
	fracPart := int64((f - float64(intPart)) * 100)
	if fracPart < 0 {
		fracPart = -fracPart
	}
	s := intToStr(intPart) + "." + padTwo(fracPart)
	if neg {
		return "-" + s
	}
	return s
}

func padTwo(v int64) string {
	if v < 10 {
		return "0" + intToStr(v)
	}
	return intToStr(v)
}
