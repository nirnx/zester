package cmdmod

import (
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/schematest"
	"github.com/nirnx/zester/pkg/state"
)

var _ state.State = (*CmdRun)(nil)

// TestCmdRunContract replays the permanent differential contract fixtures against
// the migrated cmdRunSpec decoder. The cases were approved by the legacy-vs-new
// equivalence comparison while the legacy constructor still existed (see the
// migration changelog); after its deletion this replay is the permanent
// regression guard for cmd.run's decode behavior — including the flagged BD-5 (an
// args StringList scalar element is rendered to a string, a nested element is
// rejected, a bare-string value activates a single-element list, and a composite
// env StringMap value is rejected), BD-6 (a wrong-typed command/cwd/creates is
// coerced or rejected instead of silently zeroing), and BD-8 (the `name` alias
// runs the named command — the Salt idiom — with command-beats-name precedence
// and empty-command fall-through to name) divergences.
func TestCmdRunContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var c CmdRun
		if _, err := cmdRunSpec.Decode(id, config, &c, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &c, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/cmd.run.yaml")
}
