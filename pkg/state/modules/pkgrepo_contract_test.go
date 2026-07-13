package modules

import (
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/schematest"
)

// TestPkgrepoManagedContract replays the permanent differential contract fixtures
// against the migrated pkgrepo.managed decoder. The cases were approved by the
// legacy-vs-new equivalence comparison while the legacy constructor still existed
// (see the migration changelog); after its deletion this replay is the permanent
// regression guard for pkgrepo's decode behavior — a parameter-decode-only
// migration whose Check/Apply/Revert and deferred in-place key-rotation detection
// are unchanged. The decode wrapper runs deriveHumanName so the contract can
// project the DERIVED HumanName facet exactly as the builder computes it
// (mirroring user.present's resolveGroupFacets projection). It pins the flagged
// BD-2 (string coercions for enabled/gpgcheck/refresh), BD-6 (numeric
// name/humanname/baseurl/ppa/file/key_url coerced, composite rejected), and BD-7
// (integer enabled/gpgcheck/refresh 1/0, other ints a typed error) divergences
// per the §11 per-param pinning standard.
func TestPkgrepoManagedContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var r PkgrepoManaged
		if _, err := pkgrepoManagedSpec.Decode(id, config, &r, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		r.deriveHumanName()
		return &r, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/pkgrepo.managed.yaml")
}
