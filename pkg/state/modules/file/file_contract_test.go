package filemod

import (
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/schematest"
	"github.com/nirnx/zester/pkg/state"
)

var _ state.State = (*FileManaged)(nil)

// TestFileManagedContract replays the permanent differential contract fixtures
// against the migrated fileManagedSpec decoder. The cases were approved by the
// legacy-vs-new equivalence comparison while the legacy parse still existed (see
// the migration changelog); after its deletion this replay is the permanent
// regression guard for file.managed's decode behavior — including the flagged
// BD-1 (a msgpack-delivered sized-int mode is applied, not dropped to 0644 — the
// reproduced reactor bug), BD-2 (a CLI template/makedirs string is honored), and
// BD-6 (a wrong-typed value is coerced or rejected instead of silently ignored)
// divergences.
func TestFileManagedContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var f FileManaged
		if _, err := fileManagedSpec.Decode(id, config, &f, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return &f, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/file.managed.yaml")
}
