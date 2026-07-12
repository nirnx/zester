package modschema

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/modschema/paramtypes"
)

func TestDecodePrimaryAliasDefaultLazy(t *testing.T) {
	type proto struct {
		Name  string `zester:"name,primary,aliases=title"`
		Level int    `zester:"level,default=3"`
		Mode  string `zester:"mode,lazy,default=0644"`
	}
	cs := mustCompile(t, proto{})

	// Primary from id, default applied, lazy left blank + reported.
	var dst proto
	rep, err := cs.Decode("myid", map[string]any{}, &dst, DecodeOptions{})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dst.Name != "myid" || dst.Level != 3 || dst.Mode != "" {
		t.Fatalf("dst: %+v", dst)
	}
	if !reflect.DeepEqual(rep.LazyDefaults, []string{"mode"}) {
		t.Fatalf("LazyDefaults: %v", rep.LazyDefaults)
	}

	// Alias supplies the primary; explicit level.
	dst = proto{}
	if _, err := cs.Decode("myid", map[string]any{"title": "viaalias", "level": 9}, &dst, DecodeOptions{}); err != nil {
		t.Fatalf("decode alias: %v", err)
	}
	if dst.Name != "viaalias" || dst.Level != 9 {
		t.Fatalf("alias dst: %+v", dst)
	}

	// Canonical name beats alias.
	dst = proto{}
	if _, err := cs.Decode("myid", map[string]any{"name": "canon", "title": "alias"}, &dst, DecodeOptions{}); err != nil {
		t.Fatalf("decode name-wins: %v", err)
	}
	if dst.Name != "canon" {
		t.Fatalf("name should win over alias: %+v", dst)
	}
}

// TestDecodeAmendedPrimaryAndNilAbsent pins the §2.1/§2.2 amendment: the primary
// falls back to the state ID when the source key is absent OR the resolved value
// is an empty string, and a nil value (YAML null) is ABSENT for every param — it
// never reaches coercion.
func TestDecodeAmendedPrimaryAndNilAbsent(t *testing.T) {
	t.Run("empty-string primary falls back to id", func(t *testing.T) {
		type proto struct {
			Name string `zester:"name,primary"`
		}
		cs := mustCompile(t, proto{})
		var dst proto
		if _, err := cs.Decode("theid", map[string]any{"name": ""}, &dst, DecodeOptions{}); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if dst.Name != "theid" {
			t.Fatalf("empty-string primary should fall back to id, got %q", dst.Name)
		}
	})

	t.Run("nil primary falls back to id", func(t *testing.T) {
		type proto struct {
			Name string `zester:"name,primary"`
		}
		cs := mustCompile(t, proto{})
		var dst proto
		if _, err := cs.Decode("theid", map[string]any{"name": nil}, &dst, DecodeOptions{}); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if dst.Name != "theid" {
			t.Fatalf("nil primary should fall back to id, got %q", dst.Name)
		}
	})

	t.Run("nil optional stays zero without error", func(t *testing.T) {
		type proto struct {
			Name string `zester:"name,primary"`
			Opt  string `zester:"opt"`
		}
		cs := mustCompile(t, proto{})
		var dst proto
		if _, err := cs.Decode("theid", map[string]any{"opt": nil}, &dst, DecodeOptions{}); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if dst.Opt != "" {
			t.Fatalf("nil optional should stay zero, got %q", dst.Opt)
		}
	})

	t.Run("nil required is missing-required", func(t *testing.T) {
		type proto struct {
			X string `zester:"x,required"`
		}
		cs := mustCompile(t, proto{})
		var dst proto
		_, err := cs.Decode("id", map[string]any{"x": nil}, &dst, DecodeOptions{})
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Kind != ErrMissingRequired {
			t.Fatalf("nil required should be MissingRequired, got %v", err)
		}
	})

	t.Run("nil on a semantic-typed field is undeclared zero value", func(t *testing.T) {
		registerSemType(t)
		type proto struct {
			Name string `zester:"name,primary"`
			X    semVal `zester:"x"`
		}
		cs := mustCompile(t, proto{})
		var dst proto
		if _, err := cs.Decode("theid", map[string]any{"x": nil}, &dst, DecodeOptions{}); err != nil {
			t.Fatalf("decode: %v", err)
		}
		// A run semantic Decode would set parsed to fmt.Sprint(nil) ("<nil>"); the
		// zero value proves nil was treated as absent (undeclared).
		if dst.X.parsed != "" || dst.X.origin != paramtypes.OriginExplicit {
			t.Fatalf("nil semantic field should be undeclared zero value, got %+v", dst.X)
		}
	})

	t.Run("empty-string primary on a semantic type falls back to id", func(t *testing.T) {
		registerSemType(t)
		type proto struct {
			X semVal `zester:"x,primary"`
		}
		cs := mustCompile(t, proto{})
		var dst proto
		if _, err := cs.Decode("theid", map[string]any{"x": ""}, &dst, DecodeOptions{}); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if dst.X.parsed != "theid" || dst.X.origin != paramtypes.OriginPrimary {
			t.Fatalf("empty-string semantic primary should decode the id with OriginPrimary, got %+v", dst.X)
		}
	})

	t.Run("nil primary on a semantic type falls back to id", func(t *testing.T) {
		registerSemType(t)
		type proto struct {
			X semVal `zester:"x,primary"`
		}
		cs := mustCompile(t, proto{})
		var dst proto
		if _, err := cs.Decode("theid", map[string]any{"x": nil}, &dst, DecodeOptions{}); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if dst.X.parsed != "theid" || dst.X.origin != paramtypes.OriginPrimary {
			t.Fatalf("nil semantic primary should decode the id with OriginPrimary, got %+v", dst.X)
		}
	})
}

func TestDecodeRequiredMissing(t *testing.T) {
	type proto struct {
		X string `zester:"x,required"`
	}
	cs := mustCompile(t, proto{})
	var dst proto
	_, err := cs.Decode("id", map[string]any{}, &dst, DecodeOptions{})
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Kind != ErrMissingRequired {
		t.Fatalf("want MissingRequired, got %v", err)
	}
}

func TestDecodeTransactional(t *testing.T) {
	type proto struct {
		Good string `zester:"good"`
		Num  int    `zester:"num"`
	}
	cs := mustCompile(t, proto{})

	// Pre-fill dst with sentinels; a bad Num must leave dst untouched.
	dst := proto{Good: "SENTINEL", Num: 999}
	_, err := cs.Decode("id", map[string]any{"good": "newval", "num": "not-an-int"}, &dst, DecodeOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if dst.Good != "SENTINEL" || dst.Num != 999 {
		t.Fatalf("dst was partially written: %+v", dst)
	}

	// Retrying the SAME dst with valid input is safe and commits fully.
	if _, err := cs.Decode("id", map[string]any{"good": "newval", "num": 5}, &dst, DecodeOptions{}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if dst.Good != "newval" || dst.Num != 5 {
		t.Fatalf("retry dst: %+v", dst)
	}
}

func TestDecodeTransactionalMultipleErrorsJoined(t *testing.T) {
	type proto struct {
		A int `zester:"a"`
		B int `zester:"b"`
	}
	cs := mustCompile(t, proto{})
	dst := proto{A: 1, B: 2}
	_, err := cs.Decode("id", map[string]any{"a": "x", "b": "y"}, &dst, DecodeOptions{})
	if err == nil {
		t.Fatal("expected joined error")
	}
	// Both field errors present; dst untouched.
	if dst.A != 1 || dst.B != 2 {
		t.Fatalf("dst mutated: %+v", dst)
	}
	if c := strings.Count(err.Error(), "param"); c < 2 {
		t.Fatalf("expected 2 field errors joined, got: %v", err)
	}
}

func TestDecodeSensitiveRedaction(t *testing.T) {
	const secret = "hunter2-supersecret"

	// walkNoLeak asserts the FieldError's Value is redacted and that neither the
	// top-level rendered error nor ANY link on the Unwrap chain contains any
	// substring of the secret (§2.5, F1).
	walkNoLeak := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("expected error")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("secret leaked in top-level rendered error: %s", err.Error())
		}
		var fe *FieldError
		if !errors.As(err, &fe) {
			t.Fatalf("want *FieldError, got %T: %v", err, err)
		}
		if fe.Value != redactedValue {
			t.Fatalf("Value not redacted: %v", fe.Value)
		}
		for link := error(fe); link != nil; link = errors.Unwrap(link) {
			if strings.Contains(link.Error(), secret) {
				t.Fatalf("secret leaked in unwrap link %T: %s", link, link.Error())
			}
		}
	}

	// A map into a sensitive string field is a wrong-type error.
	t.Run("wrong type", func(t *testing.T) {
		type proto struct {
			Password string `zester:"password,sensitive"`
		}
		cs := mustCompile(t, proto{})
		var dst proto
		_, err := cs.Decode("id", map[string]any{"password": map[string]any{"k": secret}}, &dst, DecodeOptions{})
		walkNoLeak(t, err)
	})

	// A STRING secret failing int coercion: the coerce.go message interpolates
	// the raw value (%q), so the fix must substitute a redacted cause.
	t.Run("string secret failing coercion", func(t *testing.T) {
		type proto struct {
			Token int `zester:"token,sensitive"`
		}
		cs := mustCompile(t, proto{})
		var dst proto
		_, err := cs.Decode("id", map[string]any{"token": secret}, &dst, DecodeOptions{})
		walkNoLeak(t, err)
	})

	// The semantic-dispatch wrap path (decode.go): a semantic type whose Decode
	// error echoes the raw value must still be redacted for a sensitive field.
	t.Run("semantic dispatch wrap path", func(t *testing.T) {
		gt := reflect.TypeOf(semVal{})
		remove := paramtypes.RegisterTestType("SensSem", gt, func(in paramtypes.Input) (any, error) {
			return nil, fmt.Errorf("semantic rejected value %v", in.Raw) // deliberately echoes the secret
		}, "sensitive semantic type", map[string]any{"type": "string"})
		t.Cleanup(remove)

		type proto struct {
			X semVal `zester:"x,sensitive"`
		}
		cs := mustCompile(t, proto{})
		var dst proto
		_, err := cs.Decode("id", map[string]any{"x": secret}, &dst, DecodeOptions{})
		walkNoLeak(t, err)
	})
}

func TestDecodeUnknownKeyPolicies(t *testing.T) {
	type proto struct {
		Name string `zester:"name,aliases=title"`
	}
	cs := mustCompile(t, proto{})

	// Ignore: recorded, no error, alias not flagged, reserved not flagged.
	reserved := map[string]struct{}{"require": {}}
	var dst proto
	rep, err := cs.Decode("id", map[string]any{
		"name":    "n",
		"title":   "unused-alias", // known alias, not unknown
		"require": []any{"x"},     // reserved, not unknown
		"test":    "y",            // extra-reserved, not unknown
		"bogus":   1,              // unknown
	}, &dst, DecodeOptions{
		Unknown:       PolicyIgnore,
		Reserved:      reserved,
		ExtraReserved: []string{"test"},
	})
	if err != nil {
		t.Fatalf("ignore: %v", err)
	}
	if !reflect.DeepEqual(rep.UnknownKeys, []string{"bogus"}) {
		t.Fatalf("UnknownKeys: %v", rep.UnknownKeys)
	}

	// Warn: Warnf invoked.
	var warnings []string
	dst = proto{}
	_, err = cs.Decode("id", map[string]any{"name": "n", "bogus": 1}, &dst, DecodeOptions{
		Unknown: PolicyWarn,
		Warnf:   func(f string, a ...any) { warnings = append(warnings, fmt.Sprintf(f, a...)) },
	})
	if err != nil {
		t.Fatalf("warn: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "bogus") {
		t.Fatalf("warnings: %v", warnings)
	}

	// Error: UnknownKeyError with suggestion.
	dst = proto{}
	_, err = cs.Decode("id", map[string]any{"nam": "n"}, &dst, DecodeOptions{Unknown: PolicyError})
	var uke *UnknownKeyError
	if !errors.As(err, &uke) {
		t.Fatalf("want *UnknownKeyError, got %v", err)
	}
	if uke.Key != "nam" || uke.Suggestion != "name" {
		t.Fatalf("unknown key: key=%q suggestion=%q", uke.Key, uke.Suggestion)
	}
	if !strings.Contains(uke.Error(), "did you mean") {
		t.Fatalf("suggestion not inlined: %s", uke.Error())
	}

	// Far-off unknown key → no suggestion.
	dst = proto{}
	_, err = cs.Decode("id", map[string]any{"zzzzzz": 1}, &dst, DecodeOptions{Unknown: PolicyError})
	if !errors.As(err, &uke) || uke.Suggestion != "" {
		t.Fatalf("expected no suggestion, got %q (%v)", uke.Suggestion, err)
	}
}

func TestUnknownKeyErrorKnownSorted(t *testing.T) {
	// Fields declared out of alphabetical order; UnknownKeyError.Known must still
	// be sorted per its documented contract (F6).
	type proto struct {
		Zebra string `zester:"zebra"`
		Apple string `zester:"apple"`
		Mango string `zester:"mango"`
	}
	cs := mustCompile(t, proto{})
	var dst proto
	_, err := cs.Decode("id", map[string]any{"bogus": 1}, &dst, DecodeOptions{Unknown: PolicyError})
	var uke *UnknownKeyError
	if !errors.As(err, &uke) {
		t.Fatalf("want *UnknownKeyError, got %v", err)
	}
	if !sort.StringsAreSorted(uke.Known) {
		t.Fatalf("Known not sorted: %v", uke.Known)
	}
	if !reflect.DeepEqual(uke.Known, []string{"apple", "mango", "zebra"}) {
		t.Fatalf("Known: %v", uke.Known)
	}
}

func TestDecodeBadDst(t *testing.T) {
	type proto struct {
		X string `zester:"x"`
	}
	type other struct{ Y int }
	cs := mustCompile(t, proto{})

	if _, err := cs.Decode("id", nil, proto{}, DecodeOptions{}); err == nil {
		t.Fatal("expected error for non-pointer dst")
	}
	var np *proto
	if _, err := cs.Decode("id", nil, np, DecodeOptions{}); err == nil {
		t.Fatal("expected error for nil pointer dst")
	}
	if _, err := cs.Decode("id", nil, &other{}, DecodeOptions{}); err == nil {
		t.Fatal("expected error for wrong-typed dst")
	}
}

// --- semantic dispatch (via a paramtypes-owned test type) ---

// semVal is the distinct Go type our test semantic type dispatches for.
type semVal struct {
	parsed string
	origin paramtypes.InputOrigin
}

func registerSemType(t *testing.T) reflect.Type {
	t.Helper()
	gt := reflect.TypeOf(semVal{})
	remove := paramtypes.RegisterTestType("TestSem", gt, func(in paramtypes.Input) (any, error) {
		s := fmt.Sprint(in.Raw)
		if s == "boom" {
			return nil, errors.New("boom rejected")
		}
		if s == "wrongtype" {
			return "not a semVal", nil // wrong concrete type
		}
		return semVal{parsed: s, origin: in.Origin}, nil
	}, "test semantic type", map[string]any{"type": "string"})
	t.Cleanup(remove)
	return gt
}

func TestDecodeSemanticDispatch(t *testing.T) {
	registerSemType(t)
	type proto struct {
		X semVal `zester:"x,primary"`
		Y semVal `zester:"y,default=defval"`
	}
	cs := mustCompile(t, proto{})

	// x from explicit key; y from eager default.
	var dst proto
	if _, err := cs.Decode("theid", map[string]any{"x": "hello"}, &dst, DecodeOptions{}); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dst.X.parsed != "hello" || dst.X.origin != paramtypes.OriginExplicit {
		t.Fatalf("X: %+v", dst.X)
	}
	if dst.Y.parsed != "defval" {
		t.Fatalf("Y default: %+v", dst.Y)
	}

	// x omitted → primary receives the state id with OriginPrimary.
	dst = proto{}
	if _, err := cs.Decode("theid", map[string]any{}, &dst, DecodeOptions{}); err != nil {
		t.Fatalf("decode primary: %v", err)
	}
	if dst.X.parsed != "theid" || dst.X.origin != paramtypes.OriginPrimary {
		t.Fatalf("primary X: %+v", dst.X)
	}

	// Schema records the semantic type name.
	f := cs.Schema().Fields[0]
	if f.SemanticType != "TestSem" {
		t.Fatalf("SemanticType: %q", f.SemanticType)
	}
}

func TestDecodeSemanticErrors(t *testing.T) {
	registerSemType(t)
	type proto struct {
		X semVal `zester:"x"`
	}
	cs := mustCompile(t, proto{})

	// Semantic Decode error → ValueInvalid FieldError; dst untouched.
	dst := proto{X: semVal{parsed: "orig"}}
	_, err := cs.Decode("id", map[string]any{"x": "boom"}, &dst, DecodeOptions{})
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Kind != ErrValueInvalid {
		t.Fatalf("want ValueInvalid, got %v", err)
	}
	if dst.X.parsed != "orig" {
		t.Fatalf("dst mutated on semantic error: %+v", dst)
	}

	// Semantic type returning the wrong concrete type → WrongType FieldError.
	dst = proto{}
	_, err = cs.Decode("id", map[string]any{"x": "wrongtype"}, &dst, DecodeOptions{})
	if !errors.As(err, &fe) || fe.Kind != ErrWrongType {
		t.Fatalf("want WrongType for wrong return type, got %v", err)
	}
}

func TestDecodeSemanticDefaultInvalidIsCompileError(t *testing.T) {
	registerSemType(t)
	// A default literal that the semantic decoder rejects fails compilation.
	type proto struct {
		X semVal `zester:"x,default=boom"`
	}
	if _, err := Compile(proto{}, Doc{}); err == nil {
		t.Fatal("expected compile error for invalid semantic default")
	}
}
