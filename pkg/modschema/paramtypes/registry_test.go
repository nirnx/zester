package paramtypes

// This is an INTERNAL test (package paramtypes): the same-package trick lets it
// implement the unexported sealed() method and thus define a real, sealed
// SemanticType. It exercises the registry and ForGoType dispatch key directly.

import (
	"reflect"
	"testing"
)

// fixtureMode is a package-internal test type implementing the sealed
// SemanticType interface — provable only because this file is in package
// paramtypes.
type fixtureMode struct{}

// fixtureModeValue is the distinct Go type fixtureMode dispatches for.
type fixtureModeValue struct{ n int }

func (fixtureMode) Name() string         { return "FixtureMode" }
func (fixtureMode) GoType() reflect.Type { return reflect.TypeOf(fixtureModeValue{}) }
func (fixtureMode) Decode(in Input) (any, error) {
	return fixtureModeValue{n: len(in.Key)}, nil
}
func (fixtureMode) Doc() string                { return "test-only semantic type" }
func (fixtureMode) JSONSchema() map[string]any { return map[string]any{"type": "integer"} }
func (fixtureMode) sealed()                    {}

func TestRegisterLookupForGoTypeAll(t *testing.T) {
	ft := fixtureMode{}
	register(ft)
	t.Cleanup(func() { unregister(ft) })

	// Lookup by name.
	got, ok := Lookup("FixtureMode")
	if !ok {
		t.Fatal("Lookup: FixtureMode not found")
	}
	if got.Name() != "FixtureMode" {
		t.Fatalf("Lookup: got name %q", got.Name())
	}

	// ForGoType by the distinct dispatch type.
	byType, ok := ForGoType(reflect.TypeOf(fixtureModeValue{}))
	if !ok {
		t.Fatal("ForGoType: fixtureModeValue not found")
	}
	if byType.Name() != "FixtureMode" {
		t.Fatalf("ForGoType: got name %q", byType.Name())
	}

	// Dispatch actually reaches the type's Decode.
	v, err := byType.Decode(Input{Key: "abcd", Origin: OriginExplicit})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if mv, ok := v.(fixtureModeValue); !ok || mv.n != 4 {
		t.Fatalf("Decode: got %#v", v)
	}

	// All includes it.
	found := false
	for _, st := range All() {
		if st.Name() == "FixtureMode" {
			found = true
		}
	}
	if !found {
		t.Fatal("All: FixtureMode missing")
	}
}

func TestForGoTypeUnknown(t *testing.T) {
	if _, ok := ForGoType(reflect.TypeOf("")); ok {
		t.Fatal("ForGoType(string) unexpectedly registered")
	}
	if _, ok := Lookup("does-not-exist"); ok {
		t.Fatal("Lookup(missing) returned ok")
	}
}

func TestRegisterDuplicateNamePanics(t *testing.T) {
	ft := fixtureMode{}
	register(ft)
	t.Cleanup(func() { unregister(ft) })

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("register of duplicate name did not panic")
		}
	}()
	register(ft) // same name + same GoType
}

// distinctNameSameType shares fixtureModeValue's GoType but reports a different
// name, to prove the duplicate-GoType guard fires independently of the name.
type distinctNameSameType struct{}

func (distinctNameSameType) Name() string         { return "OtherName" }
func (distinctNameSameType) GoType() reflect.Type { return reflect.TypeOf(fixtureModeValue{}) }
func (distinctNameSameType) Decode(in Input) (any, error) {
	return fixtureModeValue{}, nil
}
func (distinctNameSameType) Doc() string                { return "" }
func (distinctNameSameType) JSONSchema() map[string]any { return nil }
func (distinctNameSameType) sealed()                    {}

func TestRegisterDuplicateGoTypePanics(t *testing.T) {
	ft := fixtureMode{}
	register(ft)
	t.Cleanup(func() { unregister(ft) })

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("register of duplicate GoType did not panic")
		}
	}()
	register(distinctNameSameType{})
}

func TestSealBlocksRegistration(t *testing.T) {
	// Run against a private registry swap so we don't seal the shared one for
	// other tests in this binary.
	restore := swapRegistryForTest()
	defer restore()

	Seal()
	if !Sealed() {
		t.Fatal("Sealed() false after Seal()")
	}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("register after Seal did not panic")
		}
	}()
	register(fixtureMode{})
}

// swapRegistryForTest saves and clears the package registry state, returning a
// restore func. Only used by the seal test, which must mutate sealedFlag.
func swapRegistryForTest() func() {
	registryMu.Lock()
	savedName := byName
	savedType := byGoType
	savedSealed := sealedFlag
	byName = map[string]SemanticType{}
	byGoType = map[reflect.Type]SemanticType{}
	sealedFlag = false
	registryMu.Unlock()
	return func() {
		registryMu.Lock()
		byName = savedName
		byGoType = savedType
		sealedFlag = savedSealed
		registryMu.Unlock()
	}
}

func TestRegisterTestTypeGuardedByTesting(t *testing.T) {
	// The seam is guarded by testing.Testing(): inside `go test` it is true, so
	// RegisterTestType is permitted; in a production binary it is false and the
	// call panics, keeping the seam inert on a live fleet. Only the permitted side
	// is observable from a unit test (no production binary is available here), so
	// this documents the guard's contract and exercises the allowed path.
	if !testing.Testing() {
		t.Fatal("testing.Testing() must be true inside go test")
	}
	remove := RegisterTestType("GuardDoc", reflect.TypeOf(fixtureModeValue{}),
		func(in Input) (any, error) { return fixtureModeValue{}, nil },
		"guard-doc test type", map[string]any{"type": "integer"})
	remove()
}

func TestRegisterTestTypeSeam(t *testing.T) {
	gt := reflect.TypeOf(fixtureModeValue{})
	remove := RegisterTestType("SeamType", gt,
		func(in Input) (any, error) { return fixtureModeValue{n: 7}, nil },
		"seam doc", map[string]any{"type": "integer"})
	defer remove()

	st, ok := ForGoType(gt)
	if !ok || st.Name() != "SeamType" {
		t.Fatalf("RegisterTestType: ForGoType got %v ok=%v", st, ok)
	}
	remove()
	if _, ok := ForGoType(gt); ok {
		t.Fatal("RegisterTestType: type still registered after remove()")
	}
}
