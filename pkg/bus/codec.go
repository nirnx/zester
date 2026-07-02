package bus

import (
	"fmt"

	"github.com/vmihailenco/msgpack/v5"
)

// Encode serializes v to MessagePack binary format.
func Encode(v any) ([]byte, error) {
	data, err := msgpack.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("bus: msgpack encode: %w", err)
	}
	return data, nil
}

// Decode deserializes MessagePack binary data into v.
// v must be a pointer.
func Decode(data []byte, v any) error {
	if err := msgpack.Unmarshal(data, v); err != nil {
		return fmt.Errorf("bus: msgpack decode: %w", err)
	}
	return nil
}

// MustEncode serializes v to MessagePack and panics on error.
// Use only for values known to be encodable at compile time (constants, simple structs).
func MustEncode(v any) []byte {
	data, err := Encode(v)
	if err != nil {
		panic(err)
	}
	return data
}
