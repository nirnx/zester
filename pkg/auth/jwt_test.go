package auth

import (
	"testing"
	"time"
)

func TestCreateOperatorJWT(t *testing.T) {
	opKP, err := GenerateKeyBundle(RoleOperator)
	if err != nil {
		t.Fatalf("generate operator: %v", err)
	}

	token, err := CreateOperatorJWT(opKP, OperatorJWTOptions{
		Name: "test-operator",
	})
	if err != nil {
		t.Fatalf("create operator JWT: %v", err)
	}
	if token == "" {
		t.Error("operator JWT is empty")
	}

	// Decode and verify.
	oc, err := DecodeOperatorJWT(token)
	if err != nil {
		t.Fatalf("decode operator JWT: %v", err)
	}
	if oc.Name != "test-operator" {
		t.Errorf("name = %s, want test-operator", oc.Name)
	}
	if oc.Subject != opKP.PublicKey {
		t.Errorf("subject = %s, want %s", oc.Subject, opKP.PublicKey)
	}
}

func TestCreateOperatorJWT_WrongRole(t *testing.T) {
	accKP, err := GenerateKeyBundle(RoleAccount)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	_, err = CreateOperatorJWT(accKP, OperatorJWTOptions{Name: "bad"})
	if err == nil {
		t.Error("expected error for wrong role")
	}
}

func TestCreateAccountJWT(t *testing.T) {
	opKP, err := GenerateKeyBundle(RoleOperator)
	if err != nil {
		t.Fatalf("generate operator: %v", err)
	}
	accKP, err := GenerateKeyBundle(RoleAccount)
	if err != nil {
		t.Fatalf("generate account: %v", err)
	}

	token, err := CreateAccountJWT(accKP, opKP, AccountJWTOptions{
		Name:     "test-account",
		MaxConns: 100,
	})
	if err != nil {
		t.Fatalf("create account JWT: %v", err)
	}

	ac, err := DecodeAccountJWT(token)
	if err != nil {
		t.Fatalf("decode account JWT: %v", err)
	}
	if ac.Name != "test-account" {
		t.Errorf("name = %s, want test-account", ac.Name)
	}
	if ac.Issuer != opKP.PublicKey {
		t.Errorf("issuer = %s, want %s", ac.Issuer, opKP.PublicKey)
	}
	if ac.Limits.Conn != 100 {
		t.Errorf("max conns = %d, want 100", ac.Limits.Conn)
	}
}

func TestCreateAccountJWT_WrongRoles(t *testing.T) {
	opKP, _ := GenerateKeyBundle(RoleOperator)
	userKP, _ := GenerateKeyBundle(RoleUser)
	accKP, _ := GenerateKeyBundle(RoleAccount)

	// Account key is wrong role.
	_, err := CreateAccountJWT(opKP, opKP, AccountJWTOptions{Name: "bad"})
	if err == nil {
		t.Error("expected error for non-account key")
	}

	// Signing key is wrong role.
	_, err = CreateAccountJWT(accKP, userKP, AccountJWTOptions{Name: "bad"})
	if err == nil {
		t.Error("expected error for non-operator signing key")
	}
}

func TestCreateUserJWT(t *testing.T) {
	accKP, err := GenerateKeyBundle(RoleAccount)
	if err != nil {
		t.Fatalf("generate account: %v", err)
	}
	userKP, err := GenerateKeyBundle(RoleUser)
	if err != nil {
		t.Fatalf("generate user: %v", err)
	}

	token, err := CreateUserJWT(userKP, accKP, UserJWTOptions{
		Name:     "test-user",
		AllowPub: []string{"zester.>"},
		AllowSub: []string{"zester.>"},
	})
	if err != nil {
		t.Fatalf("create user JWT: %v", err)
	}

	uc, err := DecodeUserJWT(token)
	if err != nil {
		t.Fatalf("decode user JWT: %v", err)
	}
	if uc.Name != "test-user" {
		t.Errorf("name = %s, want test-user", uc.Name)
	}
	if uc.Subject != userKP.PublicKey {
		t.Errorf("subject = %s, want %s", uc.Subject, userKP.PublicKey)
	}
}

func TestCreateUserJWT_WithExpiry(t *testing.T) {
	accKP, _ := GenerateKeyBundle(RoleAccount)
	userKP, _ := GenerateKeyBundle(RoleUser)

	token, err := CreateUserJWT(userKP, accKP, UserJWTOptions{
		Name:   "expiring-user",
		Expiry: 1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("create user JWT: %v", err)
	}

	uc, err := DecodeUserJWT(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if uc.Expires == 0 {
		t.Error("expected non-zero expiry")
	}

	expiryTime := time.Unix(uc.Expires, 0)
	expectedMin := time.Now().Add(59 * time.Minute)
	if expiryTime.Before(expectedMin) {
		t.Errorf("expiry %v is before expected minimum %v", expiryTime, expectedMin)
	}
}

func TestCreateUserJWT_WithDenyPermissions(t *testing.T) {
	accKP, _ := GenerateKeyBundle(RoleAccount)
	userKP, _ := GenerateKeyBundle(RoleUser)

	token, err := CreateUserJWT(userKP, accKP, UserJWTOptions{
		Name:    "restricted-user",
		DenyPub: []string{"admin.>"},
		DenySub: []string{"admin.>"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	uc, err := DecodeUserJWT(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !uc.Pub.Deny.Contains("admin.>") {
		t.Error("expected deny pub to contain admin.>")
	}
	if !uc.Sub.Deny.Contains("admin.>") {
		t.Error("expected deny sub to contain admin.>")
	}
}

func TestGenerateFullHierarchy(t *testing.T) {
	opKP, accKP, userKP, opJWT, accJWT, userJWT, err := GenerateFullHierarchy(
		"test-op", "test-acc", "test-user",
	)
	if err != nil {
		t.Fatalf("generate hierarchy: %v", err)
	}

	if opKP == nil || accKP == nil || userKP == nil {
		t.Fatal("key bundles are nil")
	}
	if opJWT == "" || accJWT == "" || userJWT == "" {
		t.Fatal("JWTs are empty")
	}

	// Validate the chain.
	if err := ValidateJWTChain(opJWT, accJWT, userJWT); err != nil {
		t.Errorf("validate chain: %v", err)
	}
}

func TestValidateJWTChain_InvalidChain(t *testing.T) {
	// Create two separate hierarchies.
	_, _, _, opJWT1, _, _, err := GenerateFullHierarchy("op1", "acc1", "user1")
	if err != nil {
		t.Fatalf("hierarchy 1: %v", err)
	}
	_, _, _, _, accJWT2, userJWT2, err := GenerateFullHierarchy("op2", "acc2", "user2")
	if err != nil {
		t.Fatalf("hierarchy 2: %v", err)
	}

	// Cross-hierarchy validation should fail.
	if err := ValidateJWTChain(opJWT1, accJWT2, userJWT2); err == nil {
		t.Error("expected error for cross-hierarchy validation")
	}
}

func TestPeelUserJWTOptions(t *testing.T) {
	opts := PeelUserJWTOptions("web-01", "AXXXX")
	if opts.Name != "web-01" {
		t.Errorf("name = %s, want web-01", opts.Name)
	}
	if opts.IssuerAccount != "AXXXX" {
		t.Errorf("issuer account = %s, want AXXXX", opts.IssuerAccount)
	}
	if len(opts.AllowPub) == 0 {
		t.Error("expected allow pub to be non-empty")
	}
	if len(opts.AllowSub) == 0 {
		t.Error("expected allow sub to be non-empty")
	}
}

func TestPeelUserJWTOptions_ACLSubjects(t *testing.T) {
	opts := PeelUserJWTOptions("web-01", "AXXXX")

	// Publish ACL: peel must be able to publish events, facts, acks, returns,
	// and scheduled results scoped to its own ID, plus scoped JetStream API
	// and KV facts access.
	wantPub := []string{
		"zester.event.web-01.>",
		"zester.fact.web-01",
		"zester.job.*.ack.web-01",
		"zester.job.*.return.web-01",
		"zester.job.*.schedule.web-01",
		// JetStream API — scoped to specific KV buckets (no broad $JS.API.>).
		"$JS.API.STREAM.INFO.KV_facts",
		"$JS.API.STREAM.INFO.KV_settings-files",
		"$JS.API.DIRECT.GET.KV_settings-files.>",
		"$JS.API.STREAM.MSG.GET.KV_settings-files",
		"$JS.API.CONSUMER.CREATE.KV_settings-files",
		"$JS.API.CONSUMER.CREATE.KV_settings-files.>",
		"$JS.API.CONSUMER.DELETE.KV_settings-files.>",
		"$JS.API.STREAM.INFO.KV_secrets",
		"$JS.API.DIRECT.GET.KV_secrets.>",
		"$JS.API.STREAM.MSG.GET.KV_secrets",
		"$JS.API.CONSUMER.CREATE.KV_secrets",
		"$JS.API.CONSUMER.CREATE.KV_secrets.>",
		"$JS.API.CONSUMER.DELETE.KV_secrets.>",
		"$KV.facts.web-01",
		"_INBOX.>",
	}
	for _, subj := range wantPub {
		if !containsStr(opts.AllowPub, subj) {
			t.Errorf("AllowPub missing %q", subj)
		}
	}

	// A peel must have NO write access to the jobs or job-returns KV buckets:
	// scheduler results flow through the peel-scoped schedule subject and are
	// persisted by the master. A fleet-wide $KV grant here would let one
	// compromised peel forge or overwrite any job record or return.
	forbiddenPub := []string{
		"$KV.jobs.>",
		"$KV.job-returns.>",
		"$JS.API.STREAM.INFO.KV_jobs",
		"$JS.API.STREAM.INFO.KV_job-returns",
		"$JS.API.DIRECT.GET.KV_jobs.>",
		"$JS.API.DIRECT.GET.KV_job-returns.>",
	}
	for _, subj := range forbiddenPub {
		if containsStr(opts.AllowPub, subj) {
			t.Errorf("AllowPub must not contain %q", subj)
		}
	}

	// Subscribe ACL: peel must receive commands, job cancel signals,
	// KV settings-files/secrets reads, and reply inbox.
	wantSub := []string{
		"zester.cmd.web-01",
		"zester.cmd.web-01.>",
		"zester.job.*.cancel",
		"$KV.settings-files.>",
		"$KV.secrets._master_curve_pub",
		"$KV.secrets.web-01",
		"_INBOX.>",
	}
	for _, subj := range wantSub {
		if !containsStr(opts.AllowSub, subj) {
			t.Errorf("AllowSub missing %q", subj)
		}
	}
}

func TestPeelUserJWTOptions_ScopedToOwnID(t *testing.T) {
	opts := PeelUserJWTOptions("db-01", "AXXXX")

	// A peel must NOT be able to publish to another peel's subjects.
	for _, subj := range opts.AllowPub {
		if subj == "zester.event.web-01.>" || subj == "zester.fact.web-01" {
			t.Errorf("AllowPub contains subject for wrong peel: %s", subj)
		}
	}
	for _, subj := range opts.AllowSub {
		if subj == "zester.cmd.web-01" {
			t.Errorf("AllowSub contains subject for wrong peel: %s", subj)
		}
	}

	// Verify db-01-specific subjects are present.
	if !containsStr(opts.AllowPub, "zester.fact.db-01") {
		t.Error("AllowPub missing zester.fact.db-01")
	}
	if !containsStr(opts.AllowSub, "zester.cmd.db-01") {
		t.Error("AllowSub missing zester.cmd.db-01")
	}
}

func TestPeelUserJWTOptions_CancelIsWildcard(t *testing.T) {
	opts := PeelUserJWTOptions("web-01", "AXXXX")

	// Cancel subscription must use wildcard JID (zester.job.*.cancel)
	// so the peel can receive cancel for any job it participates in.
	if !containsStr(opts.AllowSub, "zester.job.*.cancel") {
		t.Error("AllowSub missing zester.job.*.cancel — peel cannot receive cancel signals")
	}

	// It must NOT be scoped to a specific JID.
	for _, subj := range opts.AllowSub {
		if subj != "zester.job.*.cancel" && len(subj) > len("zester.job.") &&
			subj[:len("zester.job.")] == "zester.job." &&
			subj[len(subj)-len(".cancel"):] == ".cancel" {
			t.Errorf("AllowSub has JID-specific cancel subject: %s", subj)
		}
	}
}

func TestPeelUserJWTOptions_EventGrantsScopedToOwnOrigin(t *testing.T) {
	opts := PeelUserJWTOptions("web-01", "AXXXX")

	// The peel-scoped event pub grant must exist (event.send publishes on
	// zester.event.<ownPeelID>.>; the events stream captures it server-side).
	if !containsStr(opts.AllowPub, "zester.event.web-01.>") {
		t.Error("AllowPub missing zester.event.web-01.> — peel cannot emit events")
	}

	// A peel must NEVER get trusted-origin publishing, reactor control-plane
	// access, or fleet-wide event snooping.
	forbiddenPub := []string{
		"zester.event.>",
		"zester.event._master.>",
		"zester.event._admin.>",
		"zester.reactor.>",
	}
	for _, subj := range forbiddenPub {
		if containsStr(opts.AllowPub, subj) {
			t.Errorf("AllowPub must not contain %q — peels publish events only on their own origin", subj)
		}
	}
	if containsStr(opts.AllowSub, "zester.event.>") {
		t.Error("AllowSub must not contain zester.event.> — no cross-peel event snooping")
	}
}

func TestAdminUserJWTOptions(t *testing.T) {
	opts := AdminUserJWTOptions("AXXXX")

	if opts.Name != "zester-admin" {
		t.Errorf("name = %s, want zester-admin", opts.Name)
	}
	if opts.IssuerAccount != "AXXXX" {
		t.Errorf("issuer account = %s, want AXXXX", opts.IssuerAccount)
	}

	// Publish: admin must be able to dispatch jobs, send commands to peels,
	// emit operator events on the _admin origin, and reach the reactor
	// control plane.
	wantPub := []string{
		"zester.cmd.>",
		"zester.dispatch",
		"zester.job.>",
		"zester.event._admin.>",
		"zester.reactor.>",
		"$JS.API.>",
		"_INBOX.>",
	}
	for _, subj := range wantPub {
		if !containsStr(opts.AllowPub, subj) {
			t.Errorf("AllowPub missing %q", subj)
		}
	}

	// Subscribe: admin must be able to watch job events, watch the event
	// stream, and read KV data.
	wantSub := []string{
		"zester.job.>",
		"zester.event.>",
		"$JS.API.>",
		"$KV.>",
		"_INBOX.>",
	}
	for _, subj := range wantSub {
		if !containsStr(opts.AllowSub, subj) {
			t.Errorf("AllowSub missing %q", subj)
		}
	}
}

func TestAdminUserJWTOptions_EventAndReactorGrants(t *testing.T) {
	opts := AdminUserJWTOptions("AXXXX")

	// `zester event send` publishes on the trusted _admin origin.
	if !containsStr(opts.AllowPub, "zester.event._admin.>") {
		t.Error("AllowPub missing zester.event._admin.> — admin cannot send operator events")
	}

	// `zester reactor test` is request/reply on zester.reactor.>.
	if !containsStr(opts.AllowPub, "zester.reactor.>") {
		t.Error("AllowPub missing zester.reactor.> — admin cannot reach the reactor control plane")
	}

	// `zester event watch` subscribes to the whole event namespace.
	if !containsStr(opts.AllowSub, "zester.event.>") {
		t.Error("AllowSub missing zester.event.> — admin cannot watch events")
	}

	// Admins publish ONLY on the _admin origin: a broad zester.event.> pub
	// grant would let an operator credential forge peel or _master origins.
	forbiddenPub := []string{
		"zester.event.>",
		"zester.event._master.>",
	}
	for _, subj := range forbiddenPub {
		if containsStr(opts.AllowPub, subj) {
			t.Errorf("AllowPub must not contain %q — admins publish events only on the _admin origin", subj)
		}
	}
}

func TestAdminUserJWTOptions_CanDispatchAndCancel(t *testing.T) {
	opts := AdminUserJWTOptions("AXXXX")

	// zester.dispatch must be in publish list for job submission.
	if !containsStr(opts.AllowPub, "zester.dispatch") {
		t.Error("AllowPub missing zester.dispatch — admin cannot submit jobs")
	}

	// zester.job.> in publish covers cancel (zester.job.<jid>.cancel).
	if !containsStr(opts.AllowPub, "zester.job.>") {
		t.Error("AllowPub missing zester.job.> — admin cannot cancel jobs")
	}

	// zester.job.> in subscribe covers return watching (zester.job.<jid>.return.<peel>).
	if !containsStr(opts.AllowSub, "zester.job.>") {
		t.Error("AllowSub missing zester.job.> — admin cannot watch job returns")
	}
}

func TestMasterUserJWTOptions(t *testing.T) {
	opts := MasterUserJWTOptions("AXXXX")

	if opts.Name != "zester-master" {
		t.Errorf("name = %s, want zester-master", opts.Name)
	}
	if opts.IssuerAccount != "AXXXX" {
		t.Errorf("issuer account = %s, want AXXXX", opts.IssuerAccount)
	}

	// Master needs broad zester.> access for all operations.
	wantPub := []string{"zester.>", "$JS.API.>", "$KV.>", "_INBOX.>"}
	for _, subj := range wantPub {
		if !containsStr(opts.AllowPub, subj) {
			t.Errorf("AllowPub missing %q", subj)
		}
	}

	wantSub := []string{"zester.>", "$JS.API.>", "$KV.>", "_INBOX.>"}
	for _, subj := range wantSub {
		if !containsStr(opts.AllowSub, subj) {
			t.Errorf("AllowSub missing %q", subj)
		}
	}
}

func TestAdminUserJWTOptions_ProducesValidJWT(t *testing.T) {
	accKP, _ := GenerateKeyBundle(RoleAccount)
	userKP, _ := GenerateKeyBundle(RoleUser)

	opts := AdminUserJWTOptions(accKP.PublicKey)
	token, err := CreateUserJWT(userKP, accKP, opts)
	if err != nil {
		t.Fatalf("create admin JWT: %v", err)
	}

	uc, err := DecodeUserJWT(token)
	if err != nil {
		t.Fatalf("decode admin JWT: %v", err)
	}
	if uc.Issuer != accKP.PublicKey {
		t.Fatalf("admin JWT issuer = %s, want %s", uc.Issuer, accKP.PublicKey)
	}
	if uc.Name != "zester-admin" {
		t.Errorf("name = %s, want zester-admin", uc.Name)
	}
	if !uc.Pub.Allow.Contains("zester.dispatch") {
		t.Error("JWT pub allow missing zester.dispatch")
	}
	if !uc.Pub.Allow.Contains("zester.job.>") {
		t.Error("JWT pub allow missing zester.job.>")
	}
	if !uc.Sub.Allow.Contains("zester.job.>") {
		t.Error("JWT sub allow missing zester.job.>")
	}
}

func TestPeelUserJWTOptions_ProducesValidJWT(t *testing.T) {
	accKP, _ := GenerateKeyBundle(RoleAccount)
	userKP, _ := GenerateKeyBundle(RoleUser)

	opts := PeelUserJWTOptions("web-01", accKP.PublicKey)
	token, err := CreateUserJWT(userKP, accKP, opts)
	if err != nil {
		t.Fatalf("create peel JWT: %v", err)
	}

	uc, err := DecodeUserJWT(token)
	if err != nil {
		t.Fatalf("decode peel JWT: %v", err)
	}
	if uc.Issuer != accKP.PublicKey {
		t.Fatalf("peel JWT issuer = %s, want %s", uc.Issuer, accKP.PublicKey)
	}
	if uc.Name != "web-01" {
		t.Errorf("name = %s, want web-01", uc.Name)
	}
	if !uc.Sub.Allow.Contains("zester.job.*.cancel") {
		t.Error("JWT sub allow missing zester.job.*.cancel")
	}
	if !uc.Pub.Allow.Contains("zester.job.*.return.web-01") {
		t.Error("JWT pub allow missing zester.job.*.return.web-01")
	}
}

func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func TestDecodeInvalidJWTs(t *testing.T) {
	_, err := DecodeOperatorJWT("garbage")
	if err == nil {
		t.Error("expected error for invalid operator JWT")
	}
	_, err = DecodeAccountJWT("garbage")
	if err == nil {
		t.Error("expected error for invalid account JWT")
	}
	_, err = DecodeUserJWT("garbage")
	if err == nil {
		t.Error("expected error for invalid user JWT")
	}
}

func TestSigningKeyPair(t *testing.T) {
	kb, _ := GenerateKeyBundle(RoleOperator)
	kp, err := SigningKeyPair(kb.Seed)
	if err != nil {
		t.Fatalf("signing key pair: %v", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	if pub != kb.PublicKey {
		t.Errorf("public key mismatch")
	}
}

func TestOperatorJWT_WithSigningKeys(t *testing.T) {
	opKP, _ := GenerateKeyBundle(RoleOperator)
	sigKP, _ := GenerateKeyBundle(RoleOperator)

	token, err := CreateOperatorJWT(opKP, OperatorJWTOptions{
		Name:        "multi-sign-op",
		SigningKeys: []string{sigKP.PublicKey},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	oc, err := DecodeOperatorJWT(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !oc.SigningKeys.Contains(sigKP.PublicKey) {
		t.Error("signing key not found in operator claims")
	}
}

func TestAccountJWT_WithSigningKeys(t *testing.T) {
	opKP, _ := GenerateKeyBundle(RoleOperator)
	accKP, _ := GenerateKeyBundle(RoleAccount)
	sigAccKP, _ := GenerateKeyBundle(RoleAccount)

	token, err := CreateAccountJWT(accKP, opKP, AccountJWTOptions{
		Name:        "multi-sign-acc",
		SigningKeys: []string{sigAccKP.PublicKey},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	ac, err := DecodeAccountJWT(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !ac.SigningKeys.Contains(sigAccKP.PublicKey) {
		t.Error("signing key not found in account claims")
	}
}

// TestPeelUserJWTOptions_UpdatePlaneGrants pins the update-plane grants the
// colocated zester-watchdog needs when connecting with the peel's creds:
// own-id command subscribe, own-key status put + bucket handle, and
// read-only update-binaries object-store download.
func TestPeelUserJWTOptions_UpdatePlaneGrants(t *testing.T) {
	opts := PeelUserJWTOptions("web-01", "AXXXX")

	contains := func(list []string, want string) bool {
		for _, s := range list {
			if s == want {
				return true
			}
		}
		return false
	}

	wantPub := []string{
		"$KV.update-status.peel.web-01",
		"$JS.API.STREAM.INFO.KV_update-status",
		"$JS.API.STREAM.INFO.OBJ_update-binaries",
		"$JS.API.DIRECT.GET.OBJ_update-binaries.>",
		"$JS.API.STREAM.MSG.GET.OBJ_update-binaries",
		"$JS.API.CONSUMER.CREATE.OBJ_update-binaries",
		"$JS.API.CONSUMER.CREATE.OBJ_update-binaries.>",
		"$JS.API.CONSUMER.DELETE.OBJ_update-binaries.>",
	}
	for _, w := range wantPub {
		if !contains(opts.AllowPub, w) {
			t.Errorf("AllowPub missing %q", w)
		}
	}
	if !contains(opts.AllowSub, "zester.update.cmd.web-01") {
		t.Errorf("AllowSub missing zester.update.cmd.web-01")
	}
	// The grants must be own-id-scoped, never fleet-wide.
	for _, banned := range []string{"$KV.update-status.>", "zester.update.cmd.>", "zester.update.>"} {
		if contains(opts.AllowPub, banned) || contains(opts.AllowSub, banned) {
			t.Errorf("grant %q must not be present (own-id scoping)", banned)
		}
	}
}
