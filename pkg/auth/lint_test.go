package auth

import "testing"

func hasRule(findings []LintFinding, rule string, sev LintSeverity) bool {
	for _, f := range findings {
		if f.Rule == rule && f.Severity == sev {
			return true
		}
	}
	return false
}

func TestLintPermissions_FlagsMissingObjectStoreFlowControl(t *testing.T) {
	// Object-store download grants WITHOUT the flow-control publish subject —
	// the exact shape of the shipped peel-creds bug.
	pub := []string{
		"$JS.API.STREAM.INFO.OBJ_update-binaries",
		"$JS.API.DIRECT.GET.OBJ_update-binaries.>",
		"$JS.API.CONSUMER.CREATE.OBJ_update-binaries",
		"$JS.API.CONSUMER.CREATE.OBJ_update-binaries.>",
		"$JS.API.CONSUMER.DELETE.OBJ_update-binaries.>",
	}
	sub := []string{"_INBOX.>"}

	findings := LintPermissions(pub, sub)
	if !hasRule(findings, "jetstream-flow-control", LintError) {
		t.Fatalf("expected an ERROR flow-control finding for the object store, got %+v", findings)
	}

	// Adding the flow-control grant clears it.
	pub = append(pub, "$JS.FC.OBJ_update-binaries.>")
	if fs := LintPermissions(pub, sub); hasRule(fs, "jetstream-flow-control", LintError) {
		t.Fatalf("flow-control finding not cleared after granting $JS.FC.OBJ_update-binaries.>: %+v", fs)
	}
}

func TestLintPermissions_KVWatchFlowControlIsWarning(t *testing.T) {
	// A KV-watch consumer without flow control is a WARN (works until
	// backpressure), not a hard error.
	pub := []string{"$JS.API.CONSUMER.CREATE.KV_settings-files.>"}
	findings := LintPermissions(pub, []string{"_INBOX.>"})
	if !hasRule(findings, "jetstream-flow-control", LintWarn) {
		t.Fatalf("expected a WARN flow-control finding for the KV stream, got %+v", findings)
	}
	if hasRule(findings, "jetstream-flow-control", LintError) {
		t.Fatalf("KV watch flow control should not be an error: %+v", findings)
	}
}

func TestLintPermissions_MissingInboxWarns(t *testing.T) {
	pub := []string{
		"$JS.API.CONSUMER.CREATE.OBJ_update-binaries.>",
		"$JS.FC.OBJ_update-binaries.>",
	}
	findings := LintPermissions(pub, nil) // no _INBOX subscribe
	if !hasRule(findings, "consumer-inbox", LintWarn) {
		t.Fatalf("expected a consumer-inbox WARN, got %+v", findings)
	}
}

func TestLintPermissions_CleanGrantsHaveNoFindings(t *testing.T) {
	pub := []string{
		"$JS.API.CONSUMER.CREATE.OBJ_update-binaries.>",
		"$JS.FC.OBJ_update-binaries.>",
		"$KV.facts.web-01",
	}
	sub := []string{"_INBOX.>", "$KV.settings-files.>"}
	if findings := LintPermissions(pub, sub); len(findings) != 0 {
		t.Fatalf("clean grants produced findings: %+v", findings)
	}
}

// TestLint_RealPeelGrantsHaveNoErrors lints the actual grant set the peel JWT
// issues. It guards the whole grant-drift class from the linter side: it would
// have FAILED before $JS.FC.OBJ_update-binaries.> was granted.
func TestLint_RealPeelGrantsHaveNoErrors(t *testing.T) {
	opts := PeelUserJWTOptions("web-01", "AXXXX")
	// The peel creds must be fully lint-clean: every stream it can create a
	// consumer on (the object store and its KV watches) carries a flow-control
	// publish grant. A finding here means a grant gap of the class that has
	// twice reached production.
	if findings := LintPermissions(opts.AllowPub, opts.AllowSub); len(findings) != 0 {
		t.Errorf("peel grants have %d lint finding(s):", len(findings))
		for _, f := range findings {
			t.Errorf("  [%s] %s: %s", f.Severity, f.Rule, f.Message)
		}
	}
}

// TestLint_MasterAndAdminGrantsAreClean guards that the trusted control-plane
// creds carry broad enough JetStream access ($JS.>) to cover their durable
// consumers' acks and flow control — the gap the sentinel caught (reactor
// event acks denied under $JS.API.>).
func TestLint_MasterAndAdminGrantsAreClean(t *testing.T) {
	for name, opts := range map[string]UserJWTOptions{
		"master": MasterUserJWTOptions("AXXXX"),
		"admin":  AdminUserJWTOptions("AXXXX"),
	} {
		if findings := LintPermissions(opts.AllowPub, opts.AllowSub); len(findings) != 0 {
			t.Errorf("%s grants have lint findings:", name)
			for _, f := range findings {
				t.Errorf("  [%s] %s: %s", f.Severity, f.Rule, f.Message)
			}
		}
	}
}

func TestSubjectAllows(t *testing.T) {
	cases := []struct {
		pattern, subject string
		want             bool
	}{
		{"$JS.FC.OBJ_update-binaries.>", "$JS.FC.OBJ_update-binaries.cons.seq", true},
		{"$JS.FC.>", "$JS.FC.OBJ_update-binaries.cons.seq", true},
		{">", "anything.at.all", true},
		{"$JS.FC.OBJ_update-binaries", "$JS.FC.OBJ_update-binaries.cons.seq", false}, // no trailing >
		{"$JS.FC.*.cons.seq", "$JS.FC.OBJ_update-binaries.cons.seq", true},
		{"$JS.FC.KV_x.>", "$JS.FC.OBJ_update-binaries.cons.seq", false},
		{"_INBOX.>", "_INBOX.reply", true},
	}
	for _, c := range cases {
		if got := subjectAllows(c.pattern, c.subject); got != c.want {
			t.Errorf("subjectAllows(%q, %q) = %v, want %v", c.pattern, c.subject, got, c.want)
		}
	}
}
