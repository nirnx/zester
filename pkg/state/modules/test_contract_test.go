package modules

import (
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/schematest"
	"github.com/nirnx/zester/pkg/state"
)

var (
	_ state.State = (*TestNop)(nil)
	_ state.State = (*TestFailWithoutChanges)(nil)
	_ state.State = (*TestSucceedWithChanges)(nil)
	_ state.State = (*TestConfigurableTestState)(nil)
)

// The three RunContract tests below replay the permanent differential contract
// fixtures against the migrated test-helper decoders. Each set of cases was
// approved by the legacy-vs-new equivalence comparison while the legacy
// constructor still existed (see the migration changelog); after its deletion
// these replays are the permanent regression guard for the decode behavior. They
// pin the flagged BD-6 (numeric comment coerced / composite comment rejected),
// BD-2 (truthy/falsy-string result/changes), and BD-7 (integer result/changes
// 1/0, other ints a typed error) divergences per the §11 per-param pinning
// standard. test.ping and test.nop declare no parameters, so they have no
// contract fixture (there is nothing to decode); their behavior is pinned by the
// unit tests in test_ping_test.go / test_helpers_test.go.

func TestTestFailWithoutChangesContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var m TestFailWithoutChanges
		if _, err := testFailWithoutChangesSpec.Decode(id, config, &m, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &m, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/test.fail_without_changes.yaml")
}

func TestTestSucceedWithChangesContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var m TestSucceedWithChanges
		if _, err := testSucceedWithChangesSpec.Decode(id, config, &m, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &m, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/test.succeed_with_changes.yaml")
}

func TestTestConfigurableTestStateContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var m TestConfigurableTestState
		if _, err := testConfigurableTestStateSpec.Decode(id, config, &m, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &m, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/test.configurable_test_state.yaml")
}
