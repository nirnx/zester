package schematest

import (
	"fmt"
	"maps"
	"reflect"
	"sort"
	"testing"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/cliargs"
	yaml "gopkg.in/yaml.v3"
)

// The three input universes a migration is differentially checked across.
const (
	UniverseYAML    = "yaml"
	UniverseMsgpack = "msgpack"
	UniverseCLI     = "cli"
)

// allUniverses is the fixed evaluation order.
var allUniverses = []string{UniverseYAML, UniverseMsgpack, UniverseCLI}

// DiffOutcome is a single decoder's result in one universe: the produced value
// and/or the error it returned. An IntentionalDiff's Expect assertion receives
// both the legacy and the new decoder's outcome so it can pin a divergence in the
// value, in the error, or in error-vs-success (the BD-6 class).
type DiffOutcome struct {
	Value any
	Err   error
}

// IntentionalDiff declares a permitted behavioral difference between the legacy
// parser and the new decoder in a given universe. BD is the stable behavioral-
// difference ID (for example "BD-2"); Changelog must be non-empty (an intentional
// fix is only intentional if it is written down). Expect asserts the specific
// divergence and returns a non-nil error if it did not manifest as declared.
type IntentionalDiff struct {
	BD        string
	Universe  string // "" or "*" applies to every universe
	Param     string
	Reason    string
	Changelog string
	Expect    func(legacy, decoded DiffOutcome) error
}

func (d IntentionalDiff) matches(universe string) bool {
	return d.Universe == "" || d.Universe == "*" || d.Universe == universe
}

// Equivalence runs the legacy parser against the new decoder over one input,
// across all three universes, and fails on any UNDECLARED divergence. Declared
// divergences (Diffs) are asserted via their Expect and required to carry a
// changelog anchor. The Legacy/Decoded outcome is any so the harness works for
// both state protos and exec-module shapes.
type Equivalence struct {
	Module     string
	ID         string
	YAMLSource string   // a real YAML mapping snippet (the whole config map)
	CLITokens  []string // optional; derived from YAMLSource scalars when omitted
	Legacy     func(id string, config map[string]any) (any, error)
	Decoded    func(id string, config map[string]any) (any, error)
	Compare    func(legacy, decoded any) error
	Diffs      []IntentionalDiff
	// Logf, when set, receives non-fatal diagnostics — notably which non-scalar
	// params the CLI universe skipped when deriving tokens (they have no CLI
	// spelling). RunEquivalence wires it to t.Logf; the t-free CheckEquivalence
	// seam leaves it nil (no logging), so a recorded-skip is surfaced, never
	// silently dropped, wherever a *testing.T is present.
	Logf func(format string, args ...any)
}

// RunEquivalence is the *testing.T wrapper around CheckEquivalence.
func RunEquivalence(t testing.TB, eq Equivalence) {
	t.Helper()
	if eq.Logf == nil {
		eq.Logf = t.Logf
	}
	for _, err := range CheckEquivalence(eq) {
		t.Error(err)
	}
}

// CheckEquivalence evaluates the differential without a *testing.T and returns
// every problem it found (undeclared divergences and diff-contract violations).
// An empty result means the migration is equivalent modulo its declared diffs.
// It is the seam the harness's own self-tests use to assert a "must fail" case.
func CheckEquivalence(eq Equivalence) []error {
	var problems []error

	if eq.Legacy == nil || eq.Decoded == nil {
		return []error{fmt.Errorf("schematest: equivalence %s: Legacy and Decoded are required", eq.Module)}
	}
	if eq.Compare == nil {
		return []error{fmt.Errorf("schematest: equivalence %s: Compare is required", eq.Module)}
	}

	base, err := unmarshalMap(eq.YAMLSource)
	if err != nil {
		return []error{fmt.Errorf("schematest: equivalence %s: parse YAMLSource: %w", eq.Module, err)}
	}

	for _, universe := range allUniverses {
		config, skip, err := eq.configFor(universe, base)
		if err != nil {
			problems = append(problems, fmt.Errorf("schematest: equivalence %s [%s]: build config: %w", eq.Module, universe, err))
			continue
		}
		if skip {
			continue
		}

		legacyVal, legacyErr := eq.Legacy(eq.ID, cloneMap(config))
		decodedVal, decodedErr := eq.Decoded(eq.ID, cloneMap(config))
		lo := DiffOutcome{Value: legacyVal, Err: legacyErr}
		do := DiffOutcome{Value: decodedVal, Err: decodedErr}

		declared := diffsFor(eq.Diffs, universe)
		if len(declared) > 0 {
			for _, d := range declared {
				if d.Changelog == "" {
					problems = append(problems, fmt.Errorf("schematest: equivalence %s [%s]: %s: IntentionalDiff requires a non-empty Changelog", eq.Module, universe, d.BD))
				}
				if d.Expect != nil {
					if err := d.Expect(lo, do); err != nil {
						problems = append(problems, fmt.Errorf("schematest: equivalence %s [%s]: %s: declared diff not observed: %w", eq.Module, universe, d.BD, err))
					}
				}
			}
			continue
		}

		// No declared diff for this universe: legacy and decoded must agree.
		switch {
		case legacyErr != nil && decodedErr != nil:
			// Both reject — equivalent.
		case legacyErr != nil || decodedErr != nil:
			problems = append(problems, fmt.Errorf("schematest: equivalence %s [%s]: undeclared divergence: legacy err=%v, decoded err=%v", eq.Module, universe, legacyErr, decodedErr))
		default:
			if err := eq.Compare(legacyVal, decodedVal); err != nil {
				problems = append(problems, fmt.Errorf("schematest: equivalence %s [%s]: undeclared divergence: %w", eq.Module, universe, err))
			}
		}
	}
	return problems
}

// configFor builds the per-universe config map. The msgpack universe round-trips
// the whole map through bus.Encode/Decode (introducing sized ints); the CLI
// universe uses CLITokens, or derives key=value tokens from the scalar entries of
// base (non-scalar params are recorded-skipped, never silently coerced).
func (eq Equivalence) configFor(universe string, base map[string]any) (map[string]any, bool, error) {
	switch universe {
	case UniverseYAML:
		return cloneMap(base), false, nil
	case UniverseMsgpack:
		data, err := bus.Encode(base)
		if err != nil {
			return nil, false, err
		}
		var out map[string]any
		if err := bus.Decode(data, &out); err != nil {
			return nil, false, err
		}
		return out, false, nil
	case UniverseCLI:
		tokens := eq.CLITokens
		if tokens == nil {
			var skipped []string
			tokens, skipped = deriveCLITokens(base)
			if len(skipped) > 0 && eq.Logf != nil {
				eq.Logf("schematest: equivalence %s [cli]: skipped non-scalar params %v (no CLI spelling)", eq.Module, skipped)
			}
		}
		out := map[string]any{}
		cliargs.ParseKeyValues(tokens, out)
		return out, false, nil
	default:
		return nil, false, fmt.Errorf("unknown universe %q", universe)
	}
}

// deriveCLITokens forms "key=value" tokens from the scalar entries of base. It
// also returns the sorted keys whose values are non-scalar and so have no CLI
// spelling — the caller reports them (never silently drops them) so a CLI
// universe with a partially-uncovered config map is visible.
func deriveCLITokens(base map[string]any) (tokens []string, skipped []string) {
	keys := make([]string, 0, len(base))
	for k := range base {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if s, err := scalarString(base[k]); err == nil {
			tokens = append(tokens, k+"="+s)
		} else {
			skipped = append(skipped, k)
		}
	}
	return tokens, skipped
}

// diffsFor returns the declared diffs applicable to a universe.
func diffsFor(diffs []IntentionalDiff, universe string) []IntentionalDiff {
	var out []IntentionalDiff
	for _, d := range diffs {
		if d.matches(universe) {
			out = append(out, d)
		}
	}
	return out
}

// unmarshalMap parses a YAML mapping snippet into map[string]any.
func unmarshalMap(src string) (map[string]any, error) {
	out := map[string]any{}
	if src == "" {
		return out, nil
	}
	if err := yaml.Unmarshal([]byte(src), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// cloneMap shallow-copies a config map so per-universe callers never observe each
// other's mutations.
func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	maps.Copy(out, m)
	return out
}

// scalarString renders a scalar for a derived CLI token; non-scalars error so the
// caller skips them.
func scalarString(v any) (string, error) {
	switch v.(type) {
	case string, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return fmt.Sprint(v), nil
	default:
		if v == nil {
			return "", fmt.Errorf("nil")
		}
		rv := reflect.ValueOf(v)
		switch rv.Kind() {
		case reflect.Map, reflect.Slice, reflect.Array, reflect.Struct, reflect.Pointer:
			return "", fmt.Errorf("non-scalar %T", v)
		default:
			return fmt.Sprint(v), nil
		}
	}
}
