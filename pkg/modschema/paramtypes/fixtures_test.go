package paramtypes_test

// This is the ONE external-test file that carries the semantic-type fixture DATA
// (spec §3). typeFixtures maps each type's Name() to its differential fixtures;
// the conformance test pins keys(typeFixtures) == names(All()) so a new sealed
// type cannot ship without fixtures, and TestTypeFixtures runs the full YAML /
// auto-msgpack / CLI matrix for every type. It lives in package paramtypes_test
// (external) because it imports schematest, which imports paramtypes — an internal
// test would be an import cycle.

import (
	"io/fs"
	"reflect"
	"sort"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/paramtypes"
	"github.com/nirnx/zester/pkg/modschema/schematest"
)

var typeFixtures = map[string][]schematest.TypeFixture{
	"FileMode": {
		// The octal-int case is the reproduced BD-1 shape: YAML `0644` parses to
		// the integer 420, whose msgpack shadow is uint16(420); both must resolve
		// to mode 0644. The CLI spells the same mode as the octal string "644".
		{Label: "octal-int-644", YAML: 420, CLI: "644", Want: paramtypes.NewFileMode(0o644)},
		{Label: "octal-string-0644", YAML: "0644", CLI: "0644", Want: paramtypes.NewFileMode(0o644)},
		// "4755" (octal) = decimal 2541 = setuid + rwxr-xr-x. yaml.v3 quotes the
		// numeric-looking string, so it stays a string through every leg.
		{Label: "setuid-4755", YAML: "4755", CLI: "4755", Want: paramtypes.NewFileMode(0o755 | fs.ModeSetuid)},
		{Label: "reject-nonoctal-string", YAML: "999", CLI: "999", WantErr: true, WantErrKind: modschema.ErrValueInvalid},
		// Review round 4: the string arm now carries the octal pattern, so a
		// word string is a SCHEMA-expressible rejection too (agreement gate
		// hard-asserts it), not just a decode-time one.
		{Label: "reject-word-string", YAML: "banana", CLI: "banana", WantErr: true, WantErrKind: modschema.ErrValueInvalid},
		{Label: "reject-mixed-octal", YAML: "0o644", CLI: "0o644", WantErr: true, WantErrKind: modschema.ErrValueInvalid},
		{Label: "reject-out-of-range-int", YAML: 5000, CLISkip: true, CLISkipReason: "an out-of-range integer mode has no equivalent octal CLI string", WantErr: true, WantErrKind: modschema.ErrValueInvalid},
		{Label: "reject-list", YAML: []any{7, 5, 5}, CLISkip: true, CLISkipReason: "a list is not a file mode", WantErr: true, WantErrKind: modschema.ErrValueInvalid},
		// EMPTY-STRING RULE (§3): "" decodes to the undeclared zero value, not an error.
		{Label: "empty-string-undeclared", YAML: "", CLISkip: true, CLISkipReason: emptyStringCLISkip, Want: paramtypes.FileMode{}},
	},
	"TriState": {
		{Label: "bool-true", YAML: true, CLI: "true", Want: paramtypes.NewTriState(true)},
		{Label: "bool-false", YAML: false, CLI: "false", Want: paramtypes.NewTriState(false)},
		{Label: "string-yes", YAML: "yes", CLI: "yes", Want: paramtypes.NewTriState(true)},
		// Decode lowercases + trims (parseBoolishString): any casing and
		// surrounding whitespace are valid — and the schema's shared boolish
		// pattern must agree (review round 3).
		{Label: "string-upper-trimmed", YAML: " TRUE ", CLI: " TRUE ", Want: paramtypes.NewTriState(true)},
		{Label: "int-one", YAML: 1, CLI: "1", Want: paramtypes.NewTriState(true)},
		{Label: "int-zero", YAML: 0, CLI: "0", Want: paramtypes.NewTriState(false)},
		{Label: "reject-two", YAML: 2, CLI: "2", WantErr: true, WantErrKind: modschema.ErrValueInvalid},
		{Label: "reject-word", YAML: "maybe", CLI: "maybe", WantErr: true, WantErrKind: modschema.ErrValueInvalid},
		// EMPTY-STRING RULE (§3): "" decodes to the undeclared zero value, not an error.
		{Label: "empty-string-undeclared", YAML: "", CLISkip: true, CLISkipReason: emptyStringCLISkip, Want: paramtypes.TriState{}},
	},
	"GroupRef": {
		// BD-4: an all-digit string resolves to the same numeric GID as the integer.
		{Label: "int-gid", YAML: 1000, CLI: "1000", Want: paramtypes.NewGroupRefGID(1000)},
		{Label: "name", YAML: "wheel", CLI: "wheel", Want: paramtypes.NewGroupRefName("wheel")},
		{Label: "reject-bool", YAML: true, CLISkip: true, CLISkipReason: "a bool is not a group; its CLI string \"true\" is a valid group name", WantErr: true, WantErrKind: modschema.ErrValueInvalid},
		// BD-4 arm: a negative GID is rejected up front (the legacy path forwarded
		// it and failed later at the group provider). CLI-skipped: "-5" is not
		// all-digits, so over the CLI it is a group NAME, not a rejected GID.
		{Label: "reject-negative-gid", YAML: -5, CLISkip: true, CLISkipReason: "a negative integer over the CLI arrives as the string \"-5\", a group name, not a numeric GID", WantErr: true, WantErrKind: modschema.ErrValueInvalid},
		// EMPTY-STRING RULE (§3): "" is undeclared (falls through to primary_group
		// at the module level), NOT a rejected empty group reference.
		{Label: "empty-string-undeclared", YAML: "", CLISkip: true, CLISkipReason: emptyStringCLISkip, Want: paramtypes.GroupRef{}},
	},
	"TemplateFlag": {
		{Label: "bool-true", YAML: true, CLI: "true", Want: paramtypes.NewTemplateFlag(true)},
		{Label: "bool-false", YAML: false, CLI: "false", Want: paramtypes.NewTemplateFlag(false)},
		{Label: "jinja", YAML: "jinja", CLI: "jinja", Want: paramtypes.NewTemplateFlag(true)},
		// BD-2: a truthy string enables rendering (the legacy `== "jinja"` dropped it).
		{Label: "truthy-string", YAML: "yes", CLI: "yes", Want: paramtypes.NewTemplateFlag(true)},
		// "jinja" matches case-insensitively and trimmed (EqualFold/TrimSpace).
		{Label: "jinja-upper-trimmed", YAML: " JINJA ", CLI: " JINJA ", Want: paramtypes.NewTemplateFlag(true)},
		// A non-jinja, non-boolish string is a hard error — and with the
		// pattern-constrained string arm the schema now rejects it too
		// (review round 3: the unconstrained arm accepted "mako").
		{Label: "reject-mako", YAML: "mako", CLI: "mako", WantErr: true, WantErrKind: modschema.ErrValueInvalid},
		{Label: "reject-number", YAML: 5, CLI: "5", WantErr: true, WantErrKind: modschema.ErrValueInvalid},
		// EMPTY-STRING RULE (§3): "" decodes to the undeclared zero value, not an error.
		{Label: "empty-string-undeclared", YAML: "", CLISkip: true, CLISkipReason: emptyStringCLISkip, Want: paramtypes.TemplateFlag{}},
	},
	"StringList": {
		{Label: "single-string", YAML: "nginx", CLI: "nginx", Want: paramtypes.StringList{"nginx"}},
		{Label: "list-of-strings", YAML: []any{"a", "b"}, CLISkip: true, CLISkipReason: "a multi-element list is not a single CLI value", Want: paramtypes.StringList{"a", "b"}},
		// BD-5 companion: scalars are stringified rather than dropped.
		{Label: "mixed-scalars", YAML: []any{"a", 2, true}, CLISkip: true, CLISkipReason: "a list is not a single CLI value", Want: paramtypes.StringList{"a", "2", "true"}},
		// BD-5: a nested element is a hard error, not a silent drop.
		{Label: "reject-nested", YAML: []any{"a", []any{"b"}}, CLISkip: true, CLISkipReason: "a nested list is not CLI-expressible", WantErr: true, WantErrKind: modschema.ErrValueInvalid},
		// EMPTY-STRING RULE (§3): "" is undeclared (the nil zero value), NOT a
		// one-element [""] list — legacy comma-ok parity.
		{Label: "empty-string-undeclared", YAML: "", CLISkip: true, CLISkipReason: emptyStringCLISkip, Want: paramtypes.StringList(nil)},
	},
	"StringMap": {
		{Label: "scalar-values", YAML: map[string]any{"a": 1, "b": "two"}, CLISkip: true, CLISkipReason: "a map is not a single CLI key=value argument", Want: paramtypes.StringMap{"a": "1", "b": "two"}},
		{Label: "reject-composite-value", YAML: map[string]any{"a": []any{"x"}}, CLISkip: true, CLISkipReason: "a map is not CLI-expressible", WantErr: true, WantErrKind: modschema.ErrValueInvalid},
		// EMPTY-STRING RULE (§3): "" decodes to the undeclared zero value, not an error.
		{Label: "empty-string-undeclared", YAML: "", CLISkip: true, CLISkipReason: emptyStringCLISkip, Want: paramtypes.StringMap(nil)},
	},
}

// emptyStringCLISkip is the shared CLISkip reason for the per-type empty-string
// EMPTY-STRING RULE fixtures: the harness CLI leg treats an empty value as
// not-provided, so the YAML and msgpack legs (both of which faithfully deliver
// the Go string "") exercise the rule; the module-level CLI leg is covered by the
// user.present contract's gid-empty-string-falls-through-cli case.
const emptyStringCLISkip = "an empty string reads as not-provided over the CLI; the YAML and msgpack legs exercise the empty-string rule"

// TestParamtypesConformance pins the seal walls: exactly VocabularySize types,
// unique names and Go types, and complete fixture coverage.
func TestParamtypesConformance(t *testing.T) {
	all := paramtypes.All()
	if len(all) != paramtypes.VocabularySize {
		t.Fatalf("All() returned %d types, want VocabularySize=%d", len(all), paramtypes.VocabularySize)
	}

	seenName := map[string]bool{}
	seenType := map[reflect.Type]bool{}
	var names []string
	for _, st := range all {
		if seenName[st.Name()] {
			t.Errorf("duplicate semantic-type Name %q", st.Name())
		}
		seenName[st.Name()] = true
		if seenType[st.GoType()] {
			t.Errorf("duplicate semantic-type GoType %s (name %q)", st.GoType(), st.Name())
		}
		seenType[st.GoType()] = true
		names = append(names, st.Name())
	}
	sort.Strings(names)

	var fxKeys []string
	for k := range typeFixtures {
		fxKeys = append(fxKeys, k)
	}
	sort.Strings(fxKeys)

	if !reflect.DeepEqual(fxKeys, names) {
		t.Fatalf("fixture keys %v != registered type names %v", fxKeys, names)
	}
}

// TestTypeFixtures runs the full differential matrix (YAML, auto-derived msgpack,
// CLI) for every registered semantic type.
func TestTypeFixtures(t *testing.T) {
	for _, st := range paramtypes.All() {
		fx, ok := typeFixtures[st.Name()]
		if !ok {
			t.Fatalf("no fixtures for type %q", st.Name())
		}
		t.Run(st.Name(), func(t *testing.T) {
			schematest.RunTypeFixtures(t, st, fx)
		})
	}
}
