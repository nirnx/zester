package modschema

import (
	"reflect"
	"testing"
)

// fuzzProto mixes every primitive target plus a primary, a required, and a
// defaulted field so FuzzDecode drives the whole plan.
type fuzzProto struct {
	S string         `zester:"s"`
	B bool           `zester:"b"`
	I int            `zester:"i"`
	U uint16         `zester:"u"`
	F float64        `zester:"f"`
	M map[string]any `zester:"m"`
	L []any          `zester:"l"`
	P string         `zester:"p,primary"`
	R int            `zester:"r,required"`
	D string         `zester:"d,default=hi"`
}

// mapCursor deterministically turns fuzz bytes into an arbitrary map[string]any
// with nested maps/slices, nils, and values of varied Go types.
type mapCursor struct {
	data []byte
	pos  int
}

func (c *mapCursor) next() byte {
	if c.pos >= len(c.data) {
		return 0
	}
	b := c.data[c.pos]
	c.pos++
	return b
}

var fuzzKeyPool = []string{
	"s", "b", "i", "u", "f", "m", "l", "p", "r", "d",
	"require", "test", "name", "nam", "bogus", "title",
}

func (c *mapCursor) key() string {
	sel := c.next()
	if sel&0x80 != 0 {
		n := int(sel&0x07) + 1
		bs := make([]byte, n)
		for i := range bs {
			bs[i] = 'a' + (c.next() % 26)
		}
		return string(bs)
	}
	return fuzzKeyPool[int(sel)%len(fuzzKeyPool)]
}

func (c *mapCursor) str() string {
	n := int(c.next() % 8)
	bs := make([]byte, n)
	for i := range bs {
		bs[i] = c.next()
	}
	return string(bs)
}

func (c *mapCursor) value(depth int) any {
	switch c.next() % 11 {
	case 0:
		return c.str()
	case 1:
		return int(int8(c.next()))
	case 2:
		return int64(c.next()) * 1000
	case 3:
		return uint16(c.next()) * 3
	case 4:
		return uint32(c.next()) * 700
	case 5:
		return c.next()&1 == 0
	case 6:
		return float64(c.next()) / 2.0
	case 7:
		return nil
	case 8:
		// Some numeric strings and truthy strings to exercise coercions.
		return []string{"true", "0755", "3.14", "off", "-5", "abc"}[int(c.next())%6]
	case 9:
		if depth < 2 {
			return c.mapVal(depth + 1)
		}
		return c.str()
	case 10:
		if depth < 2 {
			n := int(c.next()) % 4
			s := make([]any, n)
			for i := range s {
				s[i] = c.value(depth + 1)
			}
			return s
		}
		return c.str()
	}
	return nil
}

func (c *mapCursor) mapVal(depth int) map[string]any {
	m := map[string]any{}
	n := int(c.next()) % 6
	for range n {
		m[c.key()] = c.value(depth)
	}
	return m
}

func buildFuzzMap(data []byte) map[string]any {
	c := &mapCursor{data: data}
	return c.mapVal(0)
}

// FuzzDecode asserts Decode never panics and, when it returns an error, never
// partially writes dst (transactionality) — regardless of the arbitrary map
// shape or unknown-key policy. Its seed corpus runs under normal `go test`.
func FuzzDecode(f *testing.F) {
	f.Add([]byte(nil))
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	f.Add([]byte{0x80, 1, 2, 0x81, 3, 4, 5, 6, 7, 8, 9, 10})
	f.Add([]byte{2, 0, 8, 3, 9, 4, 0, 1, 5, 6, 7, 0xff})

	cs, err := Compile(fuzzProto{}, Doc{})
	if err != nil {
		f.Fatalf("compile: %v", err)
	}
	policies := []UnknownPolicy{PolicyIgnore, PolicyWarn, PolicyError}

	f.Fuzz(func(t *testing.T, data []byte) {
		config := buildFuzzMap(data)
		pol := PolicyIgnore
		if len(data) > 0 {
			pol = policies[int(data[0])%len(policies)]
		}
		// Fresh sentinel each run; a NEW map/slice so a committed decode is
		// detectably different from the sentinel.
		sentinel := fuzzProto{
			S: "S", B: true, I: -1, U: 9, F: 3.14,
			M: map[string]any{"k": "v"}, L: []any{"x"},
			P: "P", R: -2, D: "D",
		}
		dst := sentinel
		_, derr := cs.Decode("fuzzid", config, &dst, DecodeOptions{
			Unknown:       pol,
			Warnf:         func(string, ...any) {},
			Reserved:      map[string]struct{}{"require": {}},
			ExtraReserved: []string{"test"},
		})
		if derr != nil && !reflect.DeepEqual(dst, sentinel) {
			t.Fatalf("dst mutated on error: %+v (config=%v)", dst, config)
		}
	})
}
