package modules

import (
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/schematest"
	"github.com/nirnx/zester/pkg/state"
)

var _ state.State = (*MountMounted)(nil)

// TestMountMountedContract replays the permanent differential contract fixtures
// against the migrated mount.mounted decoder. The cases were approved by the
// legacy-vs-new equivalence comparison while the legacy constructor still existed
// (see the migration changelog); after its deletion this replay is the permanent
// regression guard for mount's decode behavior — a parameter-decode-only
// migration whose Check/Apply/Revert and deferred live-facet policy are unchanged.
// It pins the flagged BD-1 (msgpack sized-int dump/pass), BD-2 (string coercions
// for dump/pass/persist), BD-6 (wrong-typed name/device/fstype/opts coerced or
// rejected; float dump/pass coerced), and BD-7 (integer persist 1/0, other ints a
// typed error) divergences per the §11 per-param pinning standard.
func TestMountMountedContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var m MountMounted
		if _, err := mountMountedSpec.Decode(id, config, &m, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &m, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/mount.mounted.yaml")
}
