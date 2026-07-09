package cmd

import "testing"

// TestResolveApproveTargets_SelectorValidation covers the pure selector logic:
// exactly one of {enrollment ID, --peel, --all-pending} must be given, and a
// bare enrollment ID resolves to itself without touching the KV store. The
// --peel / --all-pending forms require a live store and are exercised by the
// integration suite.
func TestResolveApproveTargets_SelectorValidation(t *testing.T) {
	// No selector at all.
	if _, err := resolveApproveTargets(nil, "", false); err == nil {
		t.Error("no selector: expected an error")
	}

	// More than one selector.
	for _, tc := range []struct {
		args       []string
		peel       string
		allPending bool
	}{
		{args: []string{"enr-1"}, peel: "web-01"},
		{args: []string{"enr-1"}, allPending: true},
		{peel: "web-01", allPending: true},
		{args: []string{"enr-1"}, peel: "web-01", allPending: true},
	} {
		if _, err := resolveApproveTargets(tc.args, tc.peel, tc.allPending); err == nil {
			t.Errorf("multiple selectors %+v: expected an error", tc)
		}
	}

	// A bare enrollment ID resolves to itself (no store access).
	ids, err := resolveApproveTargets([]string{"enr-abc"}, "", false)
	if err != nil {
		t.Fatalf("single ID: %v", err)
	}
	if len(ids) != 1 || ids[0] != "enr-abc" {
		t.Errorf("single ID resolved to %v, want [enr-abc]", ids)
	}
}
