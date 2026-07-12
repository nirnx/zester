package paramtypes

import (
	"fmt"
	"reflect"
	"strconv"
)

// GroupRef is the semantic value for a group parameter that may be given either
// as a numeric GID or as a group name (for example file.managed's group /
// user.present's primary group). It accepts:
//
//   - any integer kind, taken as a numeric GID, and
//   - a string: an all-digit string is a numeric GID (BD-4 — "1000" resolves the
//     same as the integer 1000, matching how the OS treats a numeric group), any
//     other string is a group name.
type GroupRef struct {
	declared bool
	isGID    bool
	gid      int
	name     string
}

// NewGroupRefGID returns a declared, numeric GroupRef.
func NewGroupRefGID(gid int) GroupRef { return GroupRef{declared: true, isGID: true, gid: gid} }

// NewGroupRefName returns a declared, name-based GroupRef.
func NewGroupRefName(name string) GroupRef { return GroupRef{declared: true, name: name} }

// Declared reports whether a group was supplied.
func (g GroupRef) Declared() bool { return g.declared }

// IsGID reports whether the reference is a numeric GID (as opposed to a name).
func (g GroupRef) IsGID() bool { return g.isGID }

// GID returns the numeric GID; meaningful only when IsGID.
func (g GroupRef) GID() int { return g.gid }

// Name returns the group name; empty when the reference is a numeric GID.
func (g GroupRef) Name() string { return g.name }

// groupRefType is the sealed SemanticType descriptor for GroupRef.
type groupRefType struct{}

func (groupRefType) Name() string         { return "GroupRef" }
func (groupRefType) GoType() reflect.Type { return reflect.TypeOf(GroupRef{}) }
func (groupRefType) Doc() string {
	return "A group reference, given as a numeric GID (any integer) or a group " +
		"name. An all-digit string is treated as a numeric GID, so \"1000\" and the " +
		"integer 1000 resolve identically."
}
func (groupRefType) JSONSchema() map[string]any {
	return map[string]any{
		"oneOf": []any{
			map[string]any{"type": "integer", "minimum": 0},
			map[string]any{"type": "string", "minLength": 1},
		},
	}
}
func (groupRefType) sealed() {}

func (groupRefType) Decode(in Input) (any, error) {
	if s, ok := in.Raw.(string); ok {
		if s == "" {
			return GroupRef{}, fmt.Errorf("paramtypes: GroupRef: empty group reference")
		}
		if isAllDigits(s) {
			gid, err := strconv.Atoi(s)
			if err != nil {
				return GroupRef{}, fmt.Errorf("paramtypes: GroupRef: %q is not a valid GID: %w", s, err)
			}
			return GroupRef{declared: true, isGID: true, gid: gid}, nil
		}
		return GroupRef{declared: true, name: s}, nil
	}
	gid, ok := intFromReflect(in.Raw)
	if !ok {
		return GroupRef{}, fmt.Errorf("paramtypes: GroupRef: cannot interpret %T as a group", in.Raw)
	}
	if gid < 0 {
		return GroupRef{}, fmt.Errorf("paramtypes: GroupRef: negative GID %d", gid)
	}
	return GroupRef{declared: true, isGID: true, gid: gid}, nil
}
