package paramtypes

import (
	"fmt"
	"io/fs"
	"reflect"
	"strconv"
)

// FileMode is the semantic value for a Unix file-permission parameter (for
// example file.managed's mode). It accepts two representations:
//
//   - a string of octal digits, e.g. "0644", "755", "4755" (parsed base-8), and
//   - an octal-int of ANY integer kind whose numeric VALUE is the octal mode
//     number — the form a YAML octal literal (`mode: 0644` → int 420) and its
//     msgpack-sized-int shadow (uint16(420)) arrive as. Honoring the sized-int
//     kinds is BD-1: the reproduced reactor bug applied 0644 for a requested 0755.
//
// The setuid/setgid/sticky bits (octal 4000/2000/1000) are honored and stored as
// the corresponding fs.FileMode flag bits, not the raw octal bits — passing
// fs.FileMode(0o4755) to Chmod would silently drop setuid. This duplicates the
// parse/compare logic in pkg/state/modules/mode.go so the module can migrate onto
// this type later without a behavior change; mode.go is untouched in this tranche.
type FileMode struct {
	declared bool
	mode     fs.FileMode
}

// NewFileMode returns a declared FileMode carrying an already-resolved mode. It
// exists so tests and (later) modules can construct a known value; the zero
// FileMode is undeclared.
func NewFileMode(mode fs.FileMode) FileMode { return FileMode{declared: true, mode: mode} }

// Declared reports whether a mode value was supplied (as opposed to the zero,
// undeclared FileMode the framework leaves for an absent lazy/defaulted field).
func (m FileMode) Declared() bool { return m.declared }

// Mode returns the resolved fs.FileMode. It is meaningful only when Declared.
func (m FileMode) Mode() fs.FileMode { return m.mode }

// Resolve returns the declared mode, or def when no mode was supplied.
func (m FileMode) Resolve(def fs.FileMode) fs.FileMode {
	if m.declared {
		return m.mode
	}
	return def
}

// Equal reports whether the declared mode agrees with other on the managed
// facets — permission bits plus setuid/setgid/sticky — ignoring file-type bits.
func (m FileMode) Equal(other fs.FileMode) bool {
	return m.mode.Perm() == other.Perm() && m.mode&modeSpecialBits == other&modeSpecialBits
}

// Octal renders the managed mode facets in familiar octal ("4755").
func (m FileMode) Octal() string {
	v := uint64(m.mode.Perm())
	if m.mode&fs.ModeSetuid != 0 {
		v |= 0o4000
	}
	if m.mode&fs.ModeSetgid != 0 {
		v |= 0o2000
	}
	if m.mode&fs.ModeSticky != 0 {
		v |= 0o1000
	}
	return strconv.FormatUint(v, 8)
}

// modeSpecialBits are the non-permission mode bits FileMode manages.
const modeSpecialBits = fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky

// fileModeType is the sealed SemanticType descriptor for FileMode.
type fileModeType struct{}

func (fileModeType) Name() string         { return "FileMode" }
func (fileModeType) GoType() reflect.Type { return reflect.TypeFor[FileMode]() }
func (fileModeType) Doc() string {
	return "A Unix file-permission mode. Accepts an octal string (\"0644\", \"755\", " +
		"\"4755\") or an integer whose value is the octal mode number (a YAML octal " +
		"literal such as 0644 parses to 420). The setuid, setgid, and sticky bits " +
		"(4000/2000/1000) are honored."
}
func (fileModeType) JSONSchema() map[string]any {
	// The string arm mirrors fileModeOctalValue exactly: base-8 ParseUint, no
	// trimming, value capped at 0o7777 — an unconstrained string accepted
	// runtime-invalid values like "999" and "banana" (review round 4).
	return map[string]any{
		"anyOf": []any{
			map[string]any{"type": "string", "pattern": `^0*[0-7]{1,4}$`},
			map[string]any{"type": "integer", "minimum": 0, "maximum": 0o7777},
		},
	}
}
func (fileModeType) sealed() {}

func (fileModeType) Decode(in Input) (any, error) {
	v, err := fileModeOctalValue(in.Raw)
	if err != nil {
		return FileMode{}, err
	}
	m, err := fileModeFromOctal(v)
	if err != nil {
		return FileMode{}, err
	}
	return FileMode{declared: true, mode: m}, nil
}

// fileModeOctalValue extracts the raw octal mode number: a string is parsed
// base-8; an integer kind is taken by value (it already IS the octal number).
func fileModeOctalValue(raw any) (uint64, error) {
	if s, ok := raw.(string); ok {
		v, err := strconv.ParseUint(s, 8, 32)
		if err != nil {
			return 0, fmt.Errorf("paramtypes: FileMode: %q is not an octal mode: %w", s, err)
		}
		return v, nil
	}
	rv := reflect.ValueOf(raw)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i := rv.Int()
		if i < 0 {
			return 0, fmt.Errorf("paramtypes: FileMode: negative mode %d", i)
		}
		return uint64(i), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint(), nil
	default:
		return 0, fmt.Errorf("paramtypes: FileMode: cannot interpret %T as a file mode", raw)
	}
}

// fileModeFromOctal converts an octal mode number into an fs.FileMode, mapping
// the special octal bits to their fs flag bits. Duplicates mode.go's logic.
func fileModeFromOctal(v uint64) (fs.FileMode, error) {
	if v > 0o7777 {
		return 0, fmt.Errorf("paramtypes: FileMode: mode %#o out of range (max 7777)", v)
	}
	m := fs.FileMode(v & 0o777)
	if v&0o4000 != 0 {
		m |= fs.ModeSetuid
	}
	if v&0o2000 != 0 {
		m |= fs.ModeSetgid
	}
	if v&0o1000 != 0 {
		m |= fs.ModeSticky
	}
	return m, nil
}
