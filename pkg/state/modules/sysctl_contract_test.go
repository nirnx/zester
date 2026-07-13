package modules

import (
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/schematest"
	"github.com/nirnx/zester/pkg/state"
)

var _ state.State = (*SysctlPresent)(nil)

// TestSysctlPresentContract replays the permanent differential contract fixtures
// against the migrated sysctl.present decoder. The cases were approved by the
// legacy-vs-new equivalence comparison while the legacy constructor still existed
// (see the migration changelog); after its deletion this replay is the permanent
// regression guard for sysctl's decode behavior. It pins BD-2 (truthy/falsy-string
// persist), BD-6 (numeric name/value coerced or composite rejected; the numeric
// `value` accept is an ERROR->ACCEPT flip on the required param), and BD-7
// (integer persist 1/0, other ints a typed error) per the §11 per-param standard.
// The require-file-provider-when-persist cross-field rule is builder-tail module
// logic and is exercised by the unit tests, not this decoder contract.
func TestSysctlPresentContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var s SysctlPresent
		if _, err := sysctlPresentSpec.Decode(id, config, &s, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &s, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/sysctl.present.yaml")
}
