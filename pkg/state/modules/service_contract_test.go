package modules

import (
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/schematest"
)

// TestSvcRunningContract and TestSvcDeadContract replay the permanent differential
// contract fixtures against the migrated TriState decoders. The cases were approved
// by the legacy-vs-new equivalence comparison while the legacy constructors still
// existed (see the migration changelog); after those constructors' deletion this
// replay is the permanent regression guard for the enable-TriState + primary-name
// decode behavior — including the flagged BD-2 (a CLI enable string is now honored)
// and BD-6 (a non-string name is coerced/rejected instead of falling back to the
// state ID) divergences.
func TestSvcRunningContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var s SvcRunning
		if _, err := svcRunningSpec.Decode(id, config, &s, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &s, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/service.running.yaml")
}

func TestSvcDeadContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var s SvcDead
		if _, err := svcDeadSpec.Decode(id, config, &s, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		// Populate the derived facet so the contract can project DisableOnApply
		// exactly as the builder computes it.
		s.DisableOnApply = s.Enable.Declared() && !s.Enable.Value()
		return &s, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/service.dead.yaml")
}
