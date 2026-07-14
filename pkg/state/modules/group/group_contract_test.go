package groupmod

import (
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/schematest"
	"github.com/nirnx/zester/pkg/state"
)

var (
	_ state.State = (*GroupPresent)(nil)
	_ state.State = (*GroupAbsent)(nil)
)

// TestGroupPresentContract and TestGroupAbsentContract replay the permanent
// differential contract fixtures against the migrated group decoders. The cases
// were approved by the legacy-vs-new equivalence comparison while the legacy
// constructors still existed (see the migration changelog); after their deletion
// this replay is the permanent regression guard for group's decode behavior —
// including the flagged BD-1 (a msgpack sized-int gid is honored), BD-2 (a
// string-form gid/system is coerced), BD-5 (members/addusers/delusers StringList
// scalar-sprint / nested-reject / bare-string arms), BD-6 (a non-string name is
// coerced/rejected; a composite gid is rejected), and BD-7 (an integer system is
// 1=true / 0=false, any other integer a typed error) divergences. group.present's
// gid is a PLAIN int (no GroupRef), so a negative gid is accepted — there is no
// BD-4.
func TestGroupPresentContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var g GroupPresent
		if _, err := groupPresentSpec.Decode(id, config, &g, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &g, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/group.present.yaml")
}

func TestGroupAbsentContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var g GroupAbsent
		if _, err := groupAbsentSpec.Decode(id, config, &g, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &g, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/group.absent.yaml")
}
