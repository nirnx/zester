package execmod

// Internal test (package execmod) so it can reach the unexported spec vars and
// decode through their compiled plans. It replays the permanent contract
// fixtures (testdata/contract/*.yaml) against each execution spec's decoder,
// pinning the argStr-compatible alias resolution — primary/aliases/request-ID
// fallback — forever (keystone spec §10). Execution functions still resolve
// arguments through argStr at runtime; these contracts guard that the spec
// decodes the SAME alias set the code documents.

import (
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/schematest"
)

// specDecoder wraps a spec's compiled plan into the schematest.Decoder shape:
// it allocates a fresh proto via NewParams and decodes into it.
func specDecoder(spec *modschema.Spec) schematest.Decoder {
	return func(id string, config map[string]any) (any, error) {
		dst := spec.NewParams()
		if _, err := spec.Decode(id, config, dst, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		return dst, nil
	}
}

func TestExecSpecContracts(t *testing.T) {
	cases := []struct {
		name string
		spec *modschema.Spec
		file string
	}{
		{"test.echo", testEchoSpec, "testdata/contract/test.echo.yaml"},
		{"pkg.version", pkgVersionSpec, "testdata/contract/pkg.version.yaml"},
		{"disk.usage", diskUsageSpec, "testdata/contract/disk.usage.yaml"},
		{"cmd.run", cmdRunExecSpec, "testdata/contract/cmd.run.yaml"},
		{"grains.item", grainsItemSpec, "testdata/contract/grains.item.yaml"},
		{"sys.doc", sysDocSpec, "testdata/contract/sys.doc.yaml"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			schematest.RunContract(t, specDecoder(c.spec), c.file)
		})
	}
}

// TestServiceSpecContract replays the ONE shared service.* contract against all
// four service specs — service.status/start/stop/restart decode through the
// same serviceNameParams shape, so the alias resolution must be identical.
func TestServiceSpecContract(t *testing.T) {
	for _, spec := range []*modschema.Spec{
		serviceStatusSpec, serviceStartSpec, serviceStopSpec, serviceRestartSpec,
	} {
		t.Run(spec.Module, func(t *testing.T) {
			schematest.RunContract(t, specDecoder(spec), "testdata/contract/service.name.yaml")
		})
	}
}
