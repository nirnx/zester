//go:build integration

package integration

import (
	"strings"
	"testing"
)

// TestSaltCompat_TestNop verifies a newly added Salt-compat state module
// (test.nop) is registered and executes end-to-end.
func TestSaltCompat_TestNop(t *testing.T) {
	results := execCLI(t, "web-01", "test.nop")
	requireSuccess(t, results, "web-01")
}

// TestSaltCompat_RemoteExec verifies the imperative remote-execution registry
// (pkg/execmod) is dispatched by the peel: sys.list_functions returns the set
// of available functions, which must include other exec functions.
func TestSaltCompat_RemoteExec(t *testing.T) {
	results := execCLI(t, "web-01", "sys.list_functions")
	r := requireSuccess(t, results, "web-01")
	if len(r.Results) == 0 {
		t.Fatal("sys.list_functions returned no results")
	}
	out := r.Results[0].Details["result"]
	if !strings.Contains(out, "pkg.version") || !strings.Contains(out, "service.restart") {
		t.Errorf("sys.list_functions output missing expected functions:\n%s", out)
	}
}

// TestSaltCompat_FileTouchIdempotent verifies a new file-surgery module works
// and is idempotent end-to-end: first run changes, second run is a no-op.
func TestSaltCompat_FileTouchIdempotent(t *testing.T) {
	path := "/tmp/zester-salt-compat-touch"
	// Clean up any prior run.
	execCLI(t, "web-01", "file.absent", path)

	first := requireSuccess(t, execCLI(t, "web-01", "file.touch", path), "web-01")
	if len(first.Results) == 0 || !first.Results[0].Changed {
		t.Errorf("first file.touch should report a change, got %+v", first.Results)
	}

	second := requireSuccess(t, execCLI(t, "web-01", "file.touch", path), "web-01")
	if len(second.Results) == 0 || second.Results[0].Changed {
		t.Errorf("second file.touch should be idempotent (no change), got %+v", second.Results)
	}
}

// TestSaltCompat_UnlessGuard verifies onlyif/unless guards short-circuit a
// state end-to-end: `unless: true` (exit 0) skips execution, so no change.
func TestSaltCompat_UnlessGuard(t *testing.T) {
	results := execCLI(t, "web-01", "cmd.run", "echo should-not-matter", "unless=true")
	r := requireSuccess(t, results, "web-01")
	if len(r.Results) == 0 {
		t.Fatal("cmd.run with unless returned no results")
	}
	if r.Results[0].Changed {
		t.Errorf("unless=true guard should skip execution (no change), got changed=true: %+v", r.Results[0])
	}
}

// TestSaltCompat_CmdRunNameAlias pins BD-8 end to end: the Salt idiom
// `cmd.run: - name: <command>` now RUNS the named command. Passing only
// `name=<command>` (no bare positional, so no state ID), cmd.run resolves its
// primary `command` from the `name` alias and executes it — the captured stdout
// carries the marker. Under the previous quietly-wrong behavior cmd.run read
// only `command` (else the empty state ID) and produced NO output.
func TestSaltCompat_CmdRunNameAlias(t *testing.T) {
	const marker = "bd8-name-alias-marker"
	results := execCLI(t, "web-01", "cmd.run", "name=echo "+marker)
	r := requireSuccess(t, results, "web-01")
	if len(r.Results) == 0 {
		t.Fatal("cmd.run name-alias returned no results")
	}
	if !strings.Contains(r.Results[0].Details["stdout"], marker) {
		t.Errorf("cmd.run name alias should run the named command (BD-8); stdout = %q, want it to contain %q",
			r.Results[0].Details["stdout"], marker)
	}
}
