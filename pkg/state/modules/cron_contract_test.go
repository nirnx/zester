package modules

import (
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/schematest"
	"github.com/nirnx/zester/pkg/state"
)

var (
	_ state.State = (*CronPresent)(nil)
	_ state.State = (*CronAbsent)(nil)
)

// TestCronPresentContract and TestCronAbsentContract replay the permanent
// differential contract fixtures against the migrated cron decoders. The cases
// were approved by the legacy-vs-new equivalence comparison while the legacy
// constructors still existed (see the migration changelog); after their deletion
// this replay is the permanent regression guard for cron's decode behavior —
// including the flagged BD-3 (an integer schedule value coerces to its string
// form, `minute: 5` -> "5", instead of being dropped to `*` — the every-minute
// bug) and BD-6 (a non-string name is coerced/rejected instead of falling back to
// the state ID) divergences.
func TestCronPresentContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var c CronPresent
		if _, err := cronPresentSpec.Decode(id, config, &c, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &c, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/cron.present.yaml")
}

func TestCronAbsentContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var c CronAbsent
		if _, err := cronAbsentSpec.Decode(id, config, &c, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &c, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/cron.absent.yaml")
}
