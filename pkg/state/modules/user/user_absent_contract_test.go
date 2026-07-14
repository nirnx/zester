package usermod

import (
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/schematest"
	"github.com/nirnx/zester/pkg/state"
)

var _ state.State = (*UserAbsent)(nil)

// TestUserAbsentContract replays the permanent differential contract fixtures
// against the migrated user.absent decoder. The cases were approved by the
// legacy-vs-new equivalence comparison while the legacy constructor still existed
// (see the migration changelog); after its deletion this replay is the permanent
// regression guard for user.absent's decode behavior — an all-primitives surface
// (name primary, purge/force plain bools). It pins the flagged BD-2 (string
// coercions for purge/force), BD-6 (numeric name coerced, composite name
// rejected), and BD-7 (integer purge/force 1/0, other ints a typed error)
// divergences per the §11 per-param pinning standard.
func TestUserAbsentContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var u UserAbsent
		if _, err := userAbsentSpec.Decode(id, config, &u, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &u, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/user.absent.yaml")
}
