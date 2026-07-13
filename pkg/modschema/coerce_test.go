package modschema

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

// coerceProto exercises every primitive target in the §2.3 table.
type coerceProto struct {
	S   string         `zester:"s"`
	B   bool           `zester:"b"`
	I   int            `zester:"i"`
	I8  int8           `zester:"i8"`
	I16 int16          `zester:"i16"`
	I32 int32          `zester:"i32"`
	I64 int64          `zester:"i64"`
	U   uint           `zester:"u"`
	U8  uint8          `zester:"u8"`
	U16 uint16         `zester:"u16"`
	U32 uint32         `zester:"u32"`
	U64 uint64         `zester:"u64"`
	F32 float32        `zester:"f32"`
	F64 float64        `zester:"f64"`
	M   map[string]any `zester:"m"`
	L   []any          `zester:"l"`
}

func mustCompile(t *testing.T, proto any) *CompiledSchema {
	t.Helper()
	cs, err := Compile(proto, Doc{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return cs
}

// decodeField decodes a single key into a fresh coerceProto and returns the
// struct plus any error.
func decodeField(t *testing.T, key string, raw any) (coerceProto, error) {
	t.Helper()
	cs := mustCompile(t, coerceProto{})
	var dst coerceProto
	_, err := cs.Decode("id", map[string]any{key: raw}, &dst, DecodeOptions{})
	return dst, err
}

func wantErrKind(t *testing.T, err error, kind ErrorKind) {
	t.Helper()
	if err == nil {
		t.Fatalf("want error kind %s, got nil", kind)
	}
	var fe *FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("want *FieldError, got %T: %v", err, err)
	}
	if fe.Kind != kind {
		t.Fatalf("want kind %s, got %s (%v)", kind, fe.Kind, err)
	}
}

func TestCoerceString(t *testing.T) {
	cases := []struct {
		raw  any
		want string
	}{
		{"hello", "hello"},
		{true, "true"},
		{5, "5"},
		{int8(-5), "-5"},
		{uint16(420), "420"},
		{1.5, "1.5"},
	}
	for _, c := range cases {
		got, err := decodeField(t, "s", c.raw)
		if err != nil {
			t.Fatalf("string %v: %v", c.raw, err)
		}
		if got.S != c.want {
			t.Fatalf("string %v: got %q want %q", c.raw, got.S, c.want)
		}
	}
	// Composites are wrong-type.
	for _, bad := range []any{map[string]any{"x": 1}, []any{1}} {
		_, err := decodeField(t, "s", bad)
		wantErrKind(t, err, ErrWrongType)
	}
	// A nil value is ABSENT for every param (§2.1 amendment): it never reaches
	// coercion, so a nil optional field decodes to its zero value with no error.
	gotNil, err := decodeField(t, "s", nil)
	if err != nil {
		t.Fatalf("nil string: expected absent (no error), got %v", err)
	}
	if gotNil.S != "" {
		t.Fatalf("nil string: expected zero value, got %q", gotNil.S)
	}
}

func TestCoerceBool(t *testing.T) {
	truthy := []any{true, "true", "TRUE", "yes", "on", "1", 1, uint8(1)}
	for _, r := range truthy {
		got, err := decodeField(t, "b", r)
		if err != nil || !got.B {
			t.Fatalf("bool %v(%T): got %v err %v", r, r, got.B, err)
		}
	}
	falsy := []any{false, "false", "No", "off", "0", 0, uint8(0)}
	for _, r := range falsy {
		got, err := decodeField(t, "b", r)
		if err != nil || got.B {
			t.Fatalf("bool %v(%T): got %v err %v", r, r, got.B, err)
		}
	}
	// int other than 0/1 → value invalid.
	_, err := decodeField(t, "b", 2)
	wantErrKind(t, err, ErrValueInvalid)
	_, err = decodeField(t, "b", uint16(7))
	wantErrKind(t, err, ErrValueInvalid)
	// bad string → value invalid.
	_, err = decodeField(t, "b", "maybe")
	wantErrKind(t, err, ErrValueInvalid)
	// float → wrong type (floats rejected).
	_, err = decodeField(t, "b", 1.0)
	wantErrKind(t, err, ErrWrongType)
	// A nil value is ABSENT for every param (§2.1 amendment): it never reaches
	// coercion, so a nil optional bool decodes to its zero value with no error.
	gotNil, err := decodeField(t, "b", nil)
	if err != nil {
		t.Fatalf("nil bool: expected absent (no error), got %v", err)
	}
	if gotNil.B {
		t.Fatalf("nil bool: expected zero value false, got true")
	}
}

func TestCoerceIntSizedKinds(t *testing.T) {
	// The empirical msgpack sized-int kinds must be honored (BD-1 substrate).
	got, err := decodeField(t, "i64", uint16(420))
	if err != nil || got.I64 != 420 {
		t.Fatalf("uint16(420)->int64: got %d err %v", got.I64, err)
	}
	got, err = decodeField(t, "i", int8(-5))
	if err != nil || got.I != -5 {
		t.Fatalf("int8(-5)->int: got %d err %v", got.I, err)
	}
	got, err = decodeField(t, "i64", uint32(70000))
	if err != nil || got.I64 != 70000 {
		t.Fatalf("uint32(70000)->int64: got %d err %v", got.I64, err)
	}
	// Into uint targets too.
	got, err = decodeField(t, "u32", uint16(420))
	if err != nil || got.U32 != 420 {
		t.Fatalf("uint16(420)->uint32: got %d err %v", got.U32, err)
	}
}

func TestCoerceIntStringsBase10(t *testing.T) {
	// "0755" is base-10 → 755, NOT octal (octal belongs to FileMode only).
	got, err := decodeField(t, "i", "0755")
	if err != nil || got.I != 755 {
		t.Fatalf(`int "0755": got %d err %v`, got.I, err)
	}
	// Hex string rejected.
	_, err = decodeField(t, "i", "0x10")
	wantErrKind(t, err, ErrValueInvalid)
	// Non-numeric rejected.
	_, err = decodeField(t, "i", "abc")
	wantErrKind(t, err, ErrValueInvalid)
}

func TestCoerceIntFloatsAndRange(t *testing.T) {
	// Finite integral float ok.
	got, err := decodeField(t, "i", 5.0)
	if err != nil || got.I != 5 {
		t.Fatalf("int 5.0: got %d err %v", got.I, err)
	}
	// Non-integral float → value invalid.
	_, err = decodeField(t, "i", 5.5)
	wantErrKind(t, err, ErrValueInvalid)
	// Overflow int8.
	_, err = decodeField(t, "i8", 420)
	wantErrKind(t, err, ErrValueInvalid)
	// Negative into uint → value invalid.
	_, err = decodeField(t, "u", -5)
	wantErrKind(t, err, ErrValueInvalid)
	_, err = decodeField(t, "u", "-5")
	wantErrKind(t, err, ErrValueInvalid)
	// bool into int → wrong type.
	_, err = decodeField(t, "i", true)
	wantErrKind(t, err, ErrWrongType)
	// NaN/Inf floats → value invalid.
	_, err = decodeField(t, "i", math.NaN())
	wantErrKind(t, err, ErrValueInvalid)
	_, err = decodeField(t, "i", math.Inf(1))
	wantErrKind(t, err, ErrValueInvalid)
}

func TestCoerceFloat(t *testing.T) {
	got, err := decodeField(t, "f64", "1.5")
	if err != nil || got.F64 != 1.5 {
		t.Fatalf(`float "1.5": got %v err %v`, got.F64, err)
	}
	got, err = decodeField(t, "f64", 7)
	if err != nil || got.F64 != 7 {
		t.Fatalf("float 7: got %v err %v", got.F64, err)
	}
	got, err = decodeField(t, "f64", uint16(420))
	if err != nil || got.F64 != 420 {
		t.Fatalf("float uint16(420): got %v err %v", got.F64, err)
	}
	// Non-numeric string → value invalid.
	_, err = decodeField(t, "f64", "abc")
	wantErrKind(t, err, ErrValueInvalid)
	// NaN string → value invalid.
	_, err = decodeField(t, "f64", "NaN")
	wantErrKind(t, err, ErrValueInvalid)
	// NaN/Inf numeric → value invalid.
	_, err = decodeField(t, "f64", math.NaN())
	wantErrKind(t, err, ErrValueInvalid)
	_, err = decodeField(t, "f64", math.Inf(-1))
	wantErrKind(t, err, ErrValueInvalid)
	// bool → wrong type.
	_, err = decodeField(t, "f64", true)
	wantErrKind(t, err, ErrWrongType)
	// float32 overflow → value invalid.
	_, err = decodeField(t, "f32", math.MaxFloat64)
	wantErrKind(t, err, ErrValueInvalid)
}

func TestCoercePassthrough(t *testing.T) {
	m := map[string]any{"a": 1, "b": "x"}
	got, err := decodeField(t, "m", m)
	if err != nil || !reflect.DeepEqual(got.M, m) {
		t.Fatalf("map passthrough: got %v err %v", got.M, err)
	}
	l := []any{1, "x", true}
	got, err = decodeField(t, "l", l)
	if err != nil || !reflect.DeepEqual(got.L, l) {
		t.Fatalf("slice passthrough: got %v err %v", got.L, err)
	}
	// Wrong shapes → wrong type.
	_, err = decodeField(t, "m", "not-a-map")
	wantErrKind(t, err, ErrWrongType)
	_, err = decodeField(t, "l", "not-a-slice")
	wantErrKind(t, err, ErrWrongType)
}
