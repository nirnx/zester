// Package schematest is the differential test harness for the module-schema
// framework. It exercises semantic types and migrated modules across every
// supported input representation — YAML values, CLI key=value strings, and
// msgpack round-trips — using the REAL ingress paths (yaml.v3 Unmarshal, the
// relocated pkg/cliargs parser, and bus.Encode/Decode) rather than synthesized
// fmt.Sprint legs.
//
// It is a TEST-SUPPORT package: it is imported only from _test.go files.
// Production code never imports it; the architecture test pins its own allowed
// imports (modschema, paramtypes, pkg/bus, pkg/cliargs, yaml.v3).
package schematest

import (
	"errors"
	"reflect"
	"testing"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/cliargs"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/paramtypes"
	yaml "gopkg.in/yaml.v3"
)

// TypeFixture is one differential case for a single semantic type. YAML is the
// value as a Go representation of what the state-file loader would produce; the
// harness runs it through a genuine yaml.v3 round-trip (YAML leg) and derives the
// msgpack leg from the parsed value (bus.Encode → bus.Decode — this is where the
// sized-int kinds such as uint16 appear). CLI is the raw key=value VALUE string
// (the harness forms the "<param>=<CLI>" token and runs it through
// pkg/cliargs.ParseKeyValues); set CLISkip with a CLISkipReason when the value
// has no faithful CLI spelling.
//
// For an accepting fixture, Want is the expected decoded field value (compared
// with reflect.DeepEqual). For a rejecting fixture, set WantErr and, when the
// classification is meaningful, WantErrKind.
type TypeFixture struct {
	Label         string
	YAML          any
	CLI           string
	CLISkip       bool
	CLISkipReason string
	Want          any
	WantErr       bool
	WantErrKind   modschema.ErrorKind
}

// fixtureParam is the canonical parameter name used by the synthetic one-field
// proto RunTypeFixtures compiles for each semantic type.
const fixtureParam = "p"

// RunTypeFixtures executes the full differential matrix for one semantic type. It
// compiles a synthetic single-field proto whose only parameter has st's Go type,
// so every leg flows through the REAL framework — Compile + CompiledSchema.Decode
// — exercising primitive-vs-semantic dispatch and yielding a typed FieldError for
// WantErrKind assertions. The msgpack leg is always auto-derived from the
// YAML-parsed value; hand-written msgpack fixtures are impossible by construction.
func RunTypeFixtures(t *testing.T, st paramtypes.SemanticType, fixtures []TypeFixture) {
	t.Helper()

	protoType := goTypeStruct(st.GoType())
	cs, err := modschema.Compile(reflect.New(protoType).Elem().Interface(), modschema.Doc{})
	if err != nil {
		t.Fatalf("schematest: compile synthetic proto for %s: %v", st.Name(), err)
	}

	for _, fx := range fixtures {
		t.Run(fx.Label, func(t *testing.T) {
			// YAML leg: a real yaml.v3 round-trip of the fixture value.
			yamlVal := yamlRoundTrip(t, fx.YAML)
			runLeg(t, "yaml", cs, protoType, yamlVal, fx)

			// msgpack leg: auto-derived from the YAML-parsed value.
			mpVal := msgpackRoundTrip(t, yamlVal)
			runLeg(t, "msgpack", cs, protoType, mpVal, fx)

			// CLI leg: a real key=value token through the production parser. A
			// fixture MUST either carry a CLI value or explicitly opt out with
			// CLISkip AND a reason — a silently absent CLI value is a coverage
			// gap masquerading as a skip, so it is reported rather than ignored.
			if fx.CLISkip {
				if fx.CLISkipReason == "" {
					t.Errorf("schematest: %s: CLISkip is set without a CLISkipReason", fx.Label)
				}
				return
			}
			if fx.CLI == "" {
				t.Errorf("schematest: %s: no CLI value and CLISkip not set — provide a CLI value, "+
					"or set CLISkip with a CLISkipReason", fx.Label)
				return
			}
			args := map[string]any{}
			cliargs.ParseKeyValues([]string{fixtureParam + "=" + fx.CLI}, args)
			cliVal, ok := args[fixtureParam]
			if !ok {
				t.Fatalf("schematest: CLI token %q did not parse to a %q value", fx.CLI, fixtureParam)
			}
			runLeg(t, "cli", cs, protoType, cliVal, fx)
		})
	}
}

// runLeg decodes {fixtureParam: value} through the compiled plan and asserts the
// fixture's expectation for this representation leg.
func runLeg(t *testing.T, leg string, cs *modschema.CompiledSchema, protoType reflect.Type, value any, fx TypeFixture) {
	t.Helper()

	dst := reflect.New(protoType)
	config := map[string]any{fixtureParam: value}
	_, err := cs.Decode("fixture-id", config, dst.Interface(), modschema.DecodeOptions{})

	if fx.WantErr {
		if err == nil {
			t.Errorf("[%s] %s: expected error for value %#v, got success", leg, fx.Label, value)
			return
		}
		if fx.WantErrKind != modschema.ErrOther {
			var fe *modschema.FieldError
			if !errors.As(err, &fe) {
				t.Errorf("[%s] %s: expected *FieldError, got %T: %v", leg, fx.Label, err, err)
				return
			}
			if fe.Kind != fx.WantErrKind {
				t.Errorf("[%s] %s: error kind = %s, want %s", leg, fx.Label, fe.Kind, fx.WantErrKind)
			}
		}
		return
	}

	if err != nil {
		t.Errorf("[%s] %s: unexpected error for value %#v: %v", leg, fx.Label, value, err)
		return
	}
	got := dst.Elem().Field(0).Interface()
	if !reflect.DeepEqual(got, fx.Want) {
		t.Errorf("[%s] %s: decoded = %#v, want %#v (input %#v)", leg, fx.Label, got, fx.Want, value)
	}
}

// goTypeStruct rebuilds the synthetic proto struct type for a semantic Go type.
// It mirrors compileSingleField's shape so a fresh destination can be allocated
// per leg (Decode is transactional and wants a typed *proto).
func goTypeStruct(goType reflect.Type) reflect.Type {
	return reflect.StructOf([]reflect.StructField{{
		Name: "P",
		Type: goType,
		Tag:  reflect.StructTag(`zester:"` + fixtureParam + `" usage:"fixture parameter"`),
	}})
}

// yamlRoundTrip marshals v to a YAML document and unmarshals it back through
// yaml.v3, yielding the value shape the production state-file loader produces.
func yamlRoundTrip(t *testing.T, v any) any {
	t.Helper()
	data, err := yaml.Marshal(v)
	if err != nil {
		t.Fatalf("schematest: yaml marshal %#v: %v", v, err)
	}
	var out any
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatalf("schematest: yaml unmarshal %q: %v", data, err)
	}
	return out
}

// msgpackRoundTrip encodes v with bus.Encode and decodes it back into any,
// reproducing msgpack's magnitude-sized integer kinds (420 → uint16, …).
func msgpackRoundTrip(t *testing.T, v any) any {
	t.Helper()
	data, err := bus.Encode(v)
	if err != nil {
		t.Fatalf("schematest: msgpack encode %#v: %v", v, err)
	}
	var out any
	if err := bus.Decode(data, &out); err != nil {
		t.Fatalf("schematest: msgpack decode: %v", err)
	}
	return out
}
