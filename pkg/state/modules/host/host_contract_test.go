package hostmod

import (
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/schematest"
	"github.com/nirnx/zester/pkg/state"
)

var (
	_ state.State = (*HostPresent)(nil)
	_ state.State = (*HostAbsent)(nil)
)

// TestHostPresentContract and TestHostAbsentContract replay the permanent
// differential contract fixtures against the migrated host decoders. The cases
// were approved by the legacy-vs-new equivalence comparison while the legacy
// constructors still existed (see the migration changelog); after their deletion
// this replay is the permanent regression guard for host's decode behavior —
// including the per-field ALIAS exemplar (the hosts-file path binds `config` with
// a `path` alias and an eager /etc/hosts default) and the flagged BD-6 (a numeric
// name/ip/config is coerced, a composite rejected; the numeric ip accept is an
// ERROR->ACCEPT flip on the required param) divergence.
func TestHostPresentContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var h HostPresent
		if _, err := hostPresentSpec.Decode(id, config, &h, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &h, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/host.present.yaml")
}

func TestHostAbsentContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var h HostAbsent
		if _, err := hostAbsentSpec.Decode(id, config, &h, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &h, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/host.absent.yaml")
}
