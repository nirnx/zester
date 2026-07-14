package modschema

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/modschema/paramtypes"
)

func fieldByName(cs *CompiledSchema, name string) *fieldPlan {
	for i := range cs.fields {
		if cs.fields[i].name == name {
			return &cs.fields[i]
		}
	}
	return nil
}

func TestCompilePlanBasics(t *testing.T) {
	type proto struct {
		Name    string `zester:"name,primary,aliases=title|label" usage:"the thing"`
		Enabled bool   `zester:"enabled,default=true"`
		Retries int    `zester:"retries,required"`
		Skip    string `zester:"-"`
		Untag   string // untagged non-struct → skipped
	}
	cs := mustCompile(t, proto{})

	if got := len(cs.fields); got != 3 {
		t.Fatalf("field count: got %d want 3", got)
	}

	name := fieldByName(cs, "name")
	if name == nil || !name.primary {
		t.Fatal("name should be primary")
	}
	if !reflect.DeepEqual(name.aliases, []string{"title", "label"}) {
		t.Fatalf("aliases: got %v", name.aliases)
	}
	if name.usage != "the thing" {
		t.Fatalf("usage: got %q", name.usage)
	}

	en := fieldByName(cs, "enabled")
	if en == nil || !en.eagerDefault {
		t.Fatal("enabled should have an eager default")
	}
	if en.defaultValue.Bool() != true {
		t.Fatalf("enabled default: got %v", en.defaultValue.Bool())
	}

	re := fieldByName(cs, "retries")
	if re == nil || !re.required {
		t.Fatal("retries should be required")
	}

	// Skip and Untag must be absent.
	if fieldByName(cs, "-") != nil || fieldByName(cs, "Skip") != nil {
		t.Fatal("zester:\"-\" field leaked into plan")
	}
}

func TestCompileSchemaDerivation(t *testing.T) {
	type proto struct {
		Name     string `zester:"name,primary" usage:"target"`
		Password string `zester:"password,sensitive,default=hunter2"`
		Mode     string `zester:"mode,lazy,default=0644"`
	}
	cs := mustCompile(t, proto{})
	ms := cs.Schema()
	if ms == nil || len(ms.Fields) != 3 {
		t.Fatalf("schema fields: %+v", ms)
	}

	byName := map[string]Field{}
	for _, f := range ms.Fields {
		byName[f.Name] = f
	}

	if !byName["name"].Primary {
		t.Fatal("name not marked primary in schema")
	}
	// Sensitive field never renders its default.
	pw := byName["password"]
	if !pw.Sensitive {
		t.Fatal("password not sensitive")
	}
	if pw.Default != "" {
		t.Fatalf("sensitive default should be blank, got %q", pw.Default)
	}
	if !pw.HasDefault {
		t.Fatal("password HasDefault should be true")
	}
	// Lazy field documents its default but is not eager.
	mode := byName["mode"]
	if !mode.Lazy || mode.Default != "0644" {
		t.Fatalf("mode field: %+v", mode)
	}
	if fp := fieldByName(cs, "mode"); fp.eagerDefault {
		t.Fatal("lazy field must not be eager")
	}
}

func TestCompileRecursion(t *testing.T) {
	type inner struct {
		A string `zester:"a"`
		B int    `zester:"b"`
	}
	type proto struct {
		inner        // untagged embedded struct → recurse
		Top   string `zester:"top"`
	}
	cs := mustCompile(t, proto{})
	if len(cs.fields) != 3 {
		t.Fatalf("recursion field count: got %d want 3", len(cs.fields))
	}
	if fieldByName(cs, "a") == nil || fieldByName(cs, "b") == nil || fieldByName(cs, "top") == nil {
		t.Fatalf("recursion missing fields: %v", cs.knownNames)
	}
}

func TestCompileErrors(t *testing.T) {
	t.Run("duplicate name", func(t *testing.T) {
		type proto struct {
			A string `zester:"dup"`
			B string `zester:"dup"`
		}
		if _, err := Compile(proto{}, Doc{}); err == nil {
			t.Fatal("expected duplicate-name error")
		}
	})
	t.Run("two primaries", func(t *testing.T) {
		type proto struct {
			A string `zester:"a,primary"`
			B string `zester:"b,primary"`
		}
		if _, err := Compile(proto{}, Doc{}); err == nil {
			t.Fatal("expected multiple-primary error")
		}
	})
	t.Run("unregistered non-primitive", func(t *testing.T) {
		type weird struct{ X int }
		type proto struct {
			W weird `zester:"w"`
		}
		_, err := Compile(proto{}, Doc{})
		if err == nil || !strings.Contains(err.Error(), "unregistered non-primitive") {
			t.Fatalf("expected unregistered-type error, got %v", err)
		}
	})
	t.Run("invalid default", func(t *testing.T) {
		type proto struct {
			N int `zester:"n,default=notanumber"`
		}
		_, err := Compile(proto{}, Doc{})
		if err == nil || !strings.Contains(err.Error(), "invalid default") {
			t.Fatalf("expected invalid-default error, got %v", err)
		}
	})
	t.Run("lazy required", func(t *testing.T) {
		type proto struct {
			X string `zester:"x,lazy,required"`
		}
		_, err := Compile(proto{}, Doc{})
		if err == nil || !strings.Contains(err.Error(), "lazy cannot be combined with required") {
			t.Fatalf("expected lazy+required error, got %v", err)
		}
	})
	t.Run("unknown tag option", func(t *testing.T) {
		type proto struct {
			X string `zester:"x,bogus"`
		}
		if _, err := Compile(proto{}, Doc{}); err == nil {
			t.Fatal("expected unknown-option error")
		}
	})
	t.Run("alias collides with a parameter name", func(t *testing.T) {
		type proto struct {
			A string `zester:"a"`
			B string `zester:"b,aliases=a"`
		}
		_, err := Compile(proto{}, Doc{})
		if err == nil || !strings.Contains(err.Error(), "collides with a parameter name") {
			t.Fatalf("expected alias-vs-name collision error, got %v", err)
		}
	})
	t.Run("alias collides with another field's alias", func(t *testing.T) {
		type proto struct {
			A string `zester:"a,aliases=x"`
			B string `zester:"b,aliases=x"`
		}
		_, err := Compile(proto{}, Doc{})
		if err == nil || !strings.Contains(err.Error(), "collides with an alias") {
			t.Fatalf("expected alias-vs-alias collision error, got %v", err)
		}
	})
	t.Run("zester tag on unexported non-embedded field", func(t *testing.T) {
		type proto struct {
			secret string `zester:"secret"` //nolint:unused // pinned compile-error case
		}
		_, err := Compile(proto{}, Doc{})
		if err == nil || !strings.Contains(err.Error(), "unexported field") {
			t.Fatalf("expected unexported-tag error, got %v", err)
		}
	})
	t.Run("non struct proto", func(t *testing.T) {
		if _, err := Compile("not a struct", Doc{}); err == nil {
			t.Fatal("expected non-struct error")
		}
	})
	t.Run("nil proto", func(t *testing.T) {
		if _, err := Compile(nil, Doc{}); err == nil {
			t.Fatal("expected nil-proto error")
		}
	})
}

func TestCompileSensitiveWriteOnly(t *testing.T) {
	gt := registerSemType(t) // registers "TestSem" for the semVal Go type
	type proto struct {
		Password string `zester:"password,sensitive"`
		Public   string `zester:"public"`
		Sem      semVal `zester:"sem,sensitive"`
	}
	cs := mustCompile(t, proto{})

	byName := map[string]Field{}
	for _, f := range cs.Schema().Fields {
		byName[f.Name] = f
	}
	if byName["password"].JSONSchema["writeOnly"] != true {
		t.Fatalf("sensitive primitive fragment missing writeOnly: %v", byName["password"].JSONSchema)
	}
	if byName["sem"].JSONSchema["writeOnly"] != true {
		t.Fatalf("sensitive semantic fragment missing writeOnly: %v", byName["sem"].JSONSchema)
	}
	if _, present := byName["public"].JSONSchema["writeOnly"]; present {
		t.Fatalf("non-sensitive fragment must not carry writeOnly: %v", byName["public"].JSONSchema)
	}

	// withWriteOnly clones: the semantic type's shared JSONSchema map (returned by
	// JSONSchema()) must not have been mutated by the injection.
	st, ok := paramtypes.ForGoType(gt)
	if !ok {
		t.Fatal("semantic type not registered")
	}
	if _, mutated := st.JSONSchema()["writeOnly"]; mutated {
		t.Fatal("semantic type's shared JSONSchema was mutated by writeOnly injection")
	}
}

func TestCompileValidNonLazyDefaultDecodedThroughFieldDecoder(t *testing.T) {
	// A default literal is decoded through the field's own decoder at compile
	// time: "0755" into an int field is base-10 (755).
	type proto struct {
		N int `zester:"n,default=0755"`
	}
	cs := mustCompile(t, proto{})
	var dst proto
	if _, err := cs.Decode("id", map[string]any{}, &dst, DecodeOptions{}); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dst.N != 755 {
		t.Fatalf("eager default: got %d want 755", dst.N)
	}
}

// ---- Member-supplied dimensions (keystone spec §13, Amendment A1) ----

type memberDimProto struct {
	Name string              `zester:"name,primary" usage:"the name"`
	Mode paramtypes.FileMode `zester:"mode,lazy,memberdefault" usage:"canonical mode"`
	Src  string              `zester:"source,memberrequired" usage:"canonical source"`
}

// TestMemberSuppliedDimensions pins the §13 member-supplied rule end to end:
// a memberdefault/memberrequired field MUST be supplied via WithDefault /
// WithRequired (validated + rendered like a tag default), supplying a FIXED
// dimension is an error, and omitting a mandatory supply is an error.
func TestMemberSuppliedDimensions(t *testing.T) {
	cs, err := Compile(memberDimProto{}, Doc{Summary: "d"},
		WithDefault("mode", "0644"), WithRequired("source", true))
	if err != nil {
		t.Fatalf("compile with member dims: %v", err)
	}
	var modeField, srcField *Field
	for i := range cs.schema.Fields {
		switch cs.schema.Fields[i].Name {
		case "mode":
			modeField = &cs.schema.Fields[i]
		case "source":
			srcField = &cs.schema.Fields[i]
		}
	}
	if modeField == nil || !modeField.HasDefault || modeField.Default != "0644" {
		t.Fatalf("member default not rendered: %+v", modeField)
	}
	if srcField == nil || !srcField.Required {
		t.Fatalf("member requiredness not applied: %+v", srcField)
	}

	// A second member supplies different values — same proto, different contract
	// dimensions, everything else identical.
	cs2, err := Compile(memberDimProto{}, Doc{Summary: "d"},
		WithDefault("mode", "0755"), WithRequired("source", false))
	if err != nil {
		t.Fatalf("compile second member: %v", err)
	}
	for i := range cs2.schema.Fields {
		if cs2.schema.Fields[i].Name == "mode" && cs2.schema.Fields[i].Default != "0755" {
			t.Fatalf("second member default: %+v", cs2.schema.Fields[i])
		}
	}

	// Mandatory: omitting a member-supplied dimension fails compile.
	if _, err := Compile(memberDimProto{}, Doc{Summary: "d"}, WithRequired("source", true)); err == nil {
		t.Fatal("missing WithDefault for a memberdefault field compiled")
	}
	if _, err := Compile(memberDimProto{}, Doc{Summary: "d"}, WithDefault("mode", "0644")); err == nil {
		t.Fatal("missing WithRequired for a memberrequired field compiled")
	}

	// FIXED dimensions have no override mechanism.
	if _, err := Compile(memberDimProto{}, Doc{Summary: "d"},
		WithDefault("mode", "0644"), WithRequired("source", true),
		WithDefault("name", "x")); err == nil {
		t.Fatal("WithDefault on a fixed-dimension field compiled")
	}
	if _, err := Compile(memberDimProto{}, Doc{Summary: "d"},
		WithDefault("mode", "0644"), WithRequired("source", true),
		WithRequired("name", true)); err == nil {
		t.Fatal("WithRequired on a fixed-dimension field compiled")
	}
	if _, err := Compile(memberDimProto{}, Doc{Summary: "d"},
		WithDefault("mode", "0644"), WithDefault("nope", "1"), WithRequired("source", true)); err == nil {
		t.Fatal("WithDefault on an unknown parameter compiled")
	}

	// An invalid member default literal is a compile error (validated by the
	// field's own decoder, like a tag default).
	if _, err := Compile(memberDimProto{}, Doc{Summary: "d"},
		WithDefault("mode", "banana"), WithRequired("source", true)); err == nil {
		t.Fatal("invalid member default literal compiled")
	}
}

type badMemberDimProto struct {
	Mode paramtypes.FileMode `zester:"mode,memberdefault,default=0644" usage:"contradiction"`
}

// TestMemberDimensionTagContradictions pins the tag-level combos.
func TestMemberDimensionTagContradictions(t *testing.T) {
	if _, err := Compile(badMemberDimProto{}, Doc{Summary: "d"}); err == nil {
		t.Fatal("memberdefault + tag default compiled")
	}
}
