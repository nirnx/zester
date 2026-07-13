package modschema

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// coerceErr is a coercion failure carrying its ErrorKind. A nil *coerceErr means
// success.
type coerceErr struct {
	kind ErrorKind
	msg  string
}

func (e *coerceErr) Error() string { return e.msg }

func wrongType(msg string) *coerceErr    { return &coerceErr{kind: ErrWrongType, msg: msg} }
func valueInvalid(msg string) *coerceErr { return &coerceErr{kind: ErrValueInvalid, msg: msg} }

// truthyStrings and falsyStrings are the case-insensitive boolean string set.
var (
	truthyStrings = map[string]bool{"true": true, "yes": true, "1": true, "on": true}
	falsyStrings  = map[string]bool{"false": true, "no": true, "0": true, "off": true}
)

// coercePrimitive coerces raw into dst per the fixed §2.3 table. dst is written
// only on success; on failure dst is untouched and a typed *coerceErr returned.
// The coercion is origin-independent.
func coercePrimitive(pk primKind, dst reflect.Value, raw any) *coerceErr {
	switch pk {
	case primString:
		return coerceString(dst, raw)
	case primBool:
		return coerceBool(dst, raw)
	case primInt:
		return coerceInt(dst, raw)
	case primFloat:
		return coerceFloat(dst, raw)
	case primMapAny:
		return coercePassthroughMap(dst, raw)
	case primSliceAny:
		return coercePassthroughSlice(dst, raw)
	default:
		return valueInvalid(fmt.Sprintf("unhandled primitive kind %d", pk))
	}
}

// coerceString: string as-is; other scalars via fmt.Sprint; composites rejected.
func coerceString(dst reflect.Value, raw any) *coerceErr {
	switch v := raw.(type) {
	case nil:
		return wrongType("nil is not a string")
	case string:
		dst.SetString(v)
		return nil
	}
	rv := reflect.ValueOf(raw)
	switch rv.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		dst.SetString(fmt.Sprint(raw))
		return nil
	default:
		return wrongType(fmt.Sprintf("cannot convert %T to string", raw))
	}
}

// coerceBool: bool; truthy/falsy string set; ints 0/1 only; floats rejected.
func coerceBool(dst reflect.Value, raw any) *coerceErr {
	switch v := raw.(type) {
	case nil:
		return wrongType("nil is not a bool")
	case bool:
		dst.SetBool(v)
		return nil
	case string:
		low := strings.ToLower(strings.TrimSpace(v))
		if truthyStrings[low] {
			dst.SetBool(true)
			return nil
		}
		if falsyStrings[low] {
			dst.SetBool(false)
			return nil
		}
		return valueInvalid(fmt.Sprintf("%q is not a boolean", v))
	}
	rv := reflect.ValueOf(raw)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return boolFromInt(dst, rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		u := rv.Uint()
		if u > 1 {
			return valueInvalid(fmt.Sprintf("integer %d is not a boolean (only 0 or 1)", u))
		}
		dst.SetBool(u == 1)
		return nil
	case reflect.Float32, reflect.Float64:
		return wrongType("floats are not accepted for boolean parameters")
	default:
		return wrongType(fmt.Sprintf("cannot convert %T to bool", raw))
	}
}

func boolFromInt(dst reflect.Value, i int64) *coerceErr {
	switch i {
	case 0:
		dst.SetBool(false)
	case 1:
		dst.SetBool(true)
	default:
		return valueInvalid(fmt.Sprintf("integer %d is not a boolean (only 0 or 1)", i))
	}
	return nil
}

// coerceInt: any int/uint kind range-checked; finite integral floats; base-10
// strings. bool rejected.
func coerceInt(dst reflect.Value, raw any) *coerceErr {
	switch dst.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i, ce := intValue(raw)
		if ce != nil {
			return ce
		}
		if dst.OverflowInt(i) {
			return valueInvalid(fmt.Sprintf("%d overflows %s", i, dst.Type()))
		}
		dst.SetInt(i)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		u, ce := uintValue(raw)
		if ce != nil {
			return ce
		}
		if dst.OverflowUint(u) {
			return valueInvalid(fmt.Sprintf("%d overflows %s", u, dst.Type()))
		}
		dst.SetUint(u)
		return nil
	default:
		return valueInvalid(fmt.Sprintf("unsupported integer kind %s", dst.Kind()))
	}
}

func intValue(raw any) (int64, *coerceErr) {
	switch v := raw.(type) {
	case nil:
		return 0, wrongType("nil is not an integer")
	case bool:
		return 0, wrongType("bool is not an integer")
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0, valueInvalid(fmt.Sprintf("%q is not a base-10 integer", v))
		}
		return i, nil
	}
	rv := reflect.ValueOf(raw)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		u := rv.Uint()
		if u > math.MaxInt64 {
			return 0, valueInvalid(fmt.Sprintf("%d overflows int64", u))
		}
		return int64(u), nil
	case reflect.Float32, reflect.Float64:
		f := rv.Float()
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return 0, valueInvalid("non-finite float is not an integer")
		}
		if f != math.Trunc(f) {
			return 0, valueInvalid(fmt.Sprintf("%v is not an integral value", f))
		}
		if f < math.MinInt64 || f >= 9223372036854775808.0 {
			return 0, valueInvalid(fmt.Sprintf("%v overflows int64", f))
		}
		return int64(f), nil
	default:
		return 0, wrongType(fmt.Sprintf("cannot convert %T to integer", raw))
	}
}

func uintValue(raw any) (uint64, *coerceErr) {
	switch v := raw.(type) {
	case nil:
		return 0, wrongType("nil is not an integer")
	case bool:
		return 0, wrongType("bool is not an integer")
	case string:
		u, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0, valueInvalid(fmt.Sprintf("%q is not a base-10 unsigned integer", v))
		}
		return u, nil
	}
	rv := reflect.ValueOf(raw)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i := rv.Int()
		if i < 0 {
			return 0, valueInvalid(fmt.Sprintf("%d is negative", i))
		}
		return uint64(i), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint(), nil
	case reflect.Float32, reflect.Float64:
		f := rv.Float()
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return 0, valueInvalid("non-finite float is not an integer")
		}
		if f != math.Trunc(f) {
			return 0, valueInvalid(fmt.Sprintf("%v is not an integral value", f))
		}
		if f < 0 || f >= 18446744073709551616.0 {
			return 0, valueInvalid(fmt.Sprintf("%v overflows uint64", f))
		}
		return uint64(f), nil
	default:
		return 0, wrongType(fmt.Sprintf("cannot convert %T to integer", raw))
	}
}

// coerceFloat: any finite numeric kind; strings via ParseFloat; NaN/Inf rejected;
// bool rejected.
func coerceFloat(dst reflect.Value, raw any) *coerceErr {
	f, ce := floatValue(raw)
	if ce != nil {
		return ce
	}
	if dst.OverflowFloat(f) {
		return valueInvalid(fmt.Sprintf("%v overflows %s", f, dst.Type()))
	}
	dst.SetFloat(f)
	return nil
}

func floatValue(raw any) (float64, *coerceErr) {
	switch v := raw.(type) {
	case nil:
		return 0, wrongType("nil is not a number")
	case bool:
		return 0, wrongType("bool is not a number")
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, valueInvalid(fmt.Sprintf("%q is not a number", v))
		}
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return 0, valueInvalid(fmt.Sprintf("%q is not a finite number", v))
		}
		return f, nil
	}
	rv := reflect.ValueOf(raw)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint()), nil
	case reflect.Float32, reflect.Float64:
		f := rv.Float()
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return 0, valueInvalid("non-finite float is not accepted")
		}
		return f, nil
	default:
		return 0, wrongType(fmt.Sprintf("cannot convert %T to number", raw))
	}
}

// coercePassthroughMap accepts a map[string]any as-is.
func coercePassthroughMap(dst reflect.Value, raw any) *coerceErr {
	m, ok := raw.(map[string]any)
	if !ok {
		return wrongType(fmt.Sprintf("cannot convert %T to map[string]any", raw))
	}
	dst.Set(reflect.ValueOf(m))
	return nil
}

// coercePassthroughSlice accepts a []any as-is.
func coercePassthroughSlice(dst reflect.Value, raw any) *coerceErr {
	s, ok := raw.([]any)
	if !ok {
		return wrongType(fmt.Sprintf("cannot convert %T to []any", raw))
	}
	dst.Set(reflect.ValueOf(s))
	return nil
}
