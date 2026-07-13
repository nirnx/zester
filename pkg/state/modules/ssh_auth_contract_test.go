package modules

import (
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/schematest"
	"github.com/nirnx/zester/pkg/state"
)

var (
	_ state.State = (*SSHAuthPresent)(nil)
	_ state.State = (*SSHAuthAbsent)(nil)
)

// TestSSHAuthPresentContract and TestSSHAuthAbsentContract replay the permanent
// differential contract fixtures against the migrated ssh_auth decoders. The cases
// were approved by the legacy-vs-new equivalence comparison while the legacy
// constructors still existed (see the migration changelog); after their deletion
// this replay is the permanent regression guard for ssh_auth's decode behavior —
// pinning the flagged BD-6 (a numeric name/user/enc/comment/config is coerced, a
// composite rejected). The name-TrimSpace and require-user-OR-config cross-field
// rule are builder-tail module logic and are exercised by the unit tests, not this
// decoder contract.
func TestSSHAuthPresentContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var s SSHAuthPresent
		if _, err := sshAuthPresentSpec.Decode(id, config, &s, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &s, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/ssh_auth.present.yaml")
}

func TestSSHAuthAbsentContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var s SSHAuthAbsent
		if _, err := sshAuthAbsentSpec.Decode(id, config, &s, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &s, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/ssh_auth.absent.yaml")
}
