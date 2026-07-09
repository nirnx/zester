//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
)

// TestEnrollApprove_PeelSelector verifies the `enroll approve --peel` selector
// resolves end-to-end over the real stack: the CLI connects with operator
// creds, reads the enrollment KV index by peel ID, and reports cleanly when no
// record exists. The assertion targets a nonexistent peel, so it is
// non-mutating and never disturbs the already-approved fleet.
func TestEnrollApprove_PeelSelector(t *testing.T) {
	ctx := context.Background()
	admin, err := stack.ServiceContainer(ctx, "admin")
	if err != nil {
		t.Fatalf("get admin container: %v", err)
	}

	exit, out, err := containerExecRaw(ctx, admin,
		[]string{"zester", "enroll", "approve", "--peel", "does-not-exist"})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if exit == 0 {
		t.Errorf("approve --peel does-not-exist succeeded, want failure; output: %s", out)
	}
	if !strings.Contains(out, "no enrollment found") {
		t.Errorf("unexpected error for unknown peel: %s", out)
	}
}
