package auth

import (
	"testing"
)

func TestAcceptPolicyString(t *testing.T) {
	tests := []struct {
		policy AcceptPolicy
		want   string
	}{
		{AcceptManual, "manual"},
		{AcceptAutoTrusted, "auto-trusted"},
		{AcceptAutoAll, "auto-all"},
		{AcceptPolicy(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.policy.String(); got != tt.want {
			t.Errorf("AcceptPolicy(%d).String() = %s, want %s", tt.policy, got, tt.want)
		}
	}
}

func TestParseAcceptPolicy(t *testing.T) {
	tests := []struct {
		input   string
		want    AcceptPolicy
		wantErr bool
	}{
		{"manual", AcceptManual, false},
		{"auto-trusted", AcceptAutoTrusted, false},
		{"auto_trusted", AcceptAutoTrusted, false},
		{"auto-all", AcceptAutoAll, false},
		{"auto_all", AcceptAutoAll, false},
		{"invalid", AcceptManual, true},
	}
	for _, tt := range tests {
		got, err := ParseAcceptPolicy(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseAcceptPolicy(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("ParseAcceptPolicy(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestKeyStateString(t *testing.T) {
	tests := []struct {
		state KeyState
		want  string
	}{
		{KeyPending, "pending"},
		{KeyAccepted, "accepted"},
		{KeyRejected, "rejected"},
		{KeyRevoked, "revoked"},
		{KeyState(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("KeyState(%d).String() = %s, want %s", tt.state, got, tt.want)
		}
	}
}

func TestKeyStore_AutoAll(t *testing.T) {
	ks := NewKeyStore(AcceptAutoAll)

	state, err := ks.SubmitKey("peel-01", "UABC123", "XABC123", "")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if state != KeyAccepted {
		t.Errorf("state = %s, want accepted", state)
	}
	if !ks.IsAccepted("peel-01") {
		t.Error("peel-01 should be accepted")
	}
}

func TestKeyStore_Manual(t *testing.T) {
	ks := NewKeyStore(AcceptManual)

	state, err := ks.SubmitKey("peel-01", "UABC123", "XABC123", "")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if state != KeyPending {
		t.Errorf("state = %s, want pending", state)
	}
	if ks.IsAccepted("peel-01") {
		t.Error("peel-01 should not be accepted yet")
	}
	if ks.PendingCount() != 1 {
		t.Errorf("pending count = %d, want 1", ks.PendingCount())
	}

	// Accept it.
	if err := ks.AcceptKey("peel-01", "admin"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !ks.IsAccepted("peel-01") {
		t.Error("peel-01 should be accepted after manual accept")
	}
	if ks.PendingCount() != 0 {
		t.Errorf("pending count = %d, want 0", ks.PendingCount())
	}
}

func TestKeyStore_Reject(t *testing.T) {
	ks := NewKeyStore(AcceptManual)
	ks.SubmitKey("peel-01", "UABC123", "XABC123", "")

	if err := ks.RejectKey("peel-01", "admin"); err != nil {
		t.Fatalf("reject: %v", err)
	}

	record, ok := ks.GetRecord("peel-01")
	if !ok {
		t.Fatal("record not found")
	}
	if record.State != KeyRejected {
		t.Errorf("state = %s, want rejected", record.State)
	}
}

func TestKeyStore_Revoke(t *testing.T) {
	ks := NewKeyStore(AcceptAutoAll)
	ks.SubmitKey("peel-01", "UABC123", "XABC123", "")

	if err := ks.RevokeKey("peel-01", "admin"); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if ks.IsAccepted("peel-01") {
		t.Error("peel-01 should not be accepted after revoke")
	}

	// Re-submit should fail.
	_, err := ks.SubmitKey("peel-01", "UABC123", "XABC123", "")
	if err == nil {
		t.Error("expected error submitting revoked key")
	}
}

func TestKeyStore_AcceptNonExistent(t *testing.T) {
	ks := NewKeyStore(AcceptManual)
	if err := ks.AcceptKey("nonexistent", "admin"); err == nil {
		t.Error("expected error accepting non-existent key")
	}
}

func TestKeyStore_AcceptAlreadyAccepted(t *testing.T) {
	ks := NewKeyStore(AcceptAutoAll)
	ks.SubmitKey("peel-01", "UABC123", "XABC123", "")

	if err := ks.AcceptKey("peel-01", "admin"); err == nil {
		t.Error("expected error accepting already-accepted key")
	}
}

func TestKeyStore_RejectNonPending(t *testing.T) {
	ks := NewKeyStore(AcceptAutoAll)
	ks.SubmitKey("peel-01", "UABC123", "XABC123", "")

	if err := ks.RejectKey("peel-01", "admin"); err == nil {
		t.Error("expected error rejecting non-pending key")
	}
}

func TestKeyStore_RevokeNonAccepted(t *testing.T) {
	ks := NewKeyStore(AcceptManual)
	ks.SubmitKey("peel-01", "UABC123", "XABC123", "")

	if err := ks.RevokeKey("peel-01", "admin"); err == nil {
		t.Error("expected error revoking non-accepted key")
	}
}

func TestKeyStore_AutoTrusted(t *testing.T) {
	accKP, _ := GenerateKeyBundle(RoleAccount)
	userKP, _ := GenerateKeyBundle(RoleUser)

	token, err := CreateUserJWT(userKP, accKP, UserJWTOptions{
		Name: "peel-trusted",
	})
	if err != nil {
		t.Fatalf("create user JWT: %v", err)
	}

	ks := NewKeyStore(AcceptAutoTrusted)
	ks.AddTrustedAccount(accKP.PublicKey)

	state, err := ks.SubmitKey("peel-01", userKP.PublicKey, "XABC", token)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if state != KeyAccepted {
		t.Errorf("state = %s, want accepted (auto-trusted)", state)
	}
}

func TestKeyStore_AutoTrusted_UntrustedAccount(t *testing.T) {
	accKP, _ := GenerateKeyBundle(RoleAccount)
	userKP, _ := GenerateKeyBundle(RoleUser)

	token, _ := CreateUserJWT(userKP, accKP, UserJWTOptions{Name: "peel-untrusted"})

	ks := NewKeyStore(AcceptAutoTrusted)
	// Don't add the account as trusted.

	state, err := ks.SubmitKey("peel-01", userKP.PublicKey, "XABC", token)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if state != KeyPending {
		t.Errorf("state = %s, want pending (untrusted account)", state)
	}
}

func TestKeyStore_AutoTrusted_NoJWT(t *testing.T) {
	ks := NewKeyStore(AcceptAutoTrusted)
	accKP, _ := GenerateKeyBundle(RoleAccount)
	ks.AddTrustedAccount(accKP.PublicKey)

	state, err := ks.SubmitKey("peel-01", "UABC", "XABC", "")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if state != KeyPending {
		t.Errorf("state = %s, want pending (no JWT)", state)
	}
}

func TestKeyStore_ResubmitAccepted(t *testing.T) {
	ks := NewKeyStore(AcceptAutoAll)
	ks.SubmitKey("peel-01", "UABC123", "XABC123", "")

	// Same key re-submitted should return accepted.
	state, err := ks.SubmitKey("peel-01", "UABC123", "XABC123", "")
	if err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if state != KeyAccepted {
		t.Errorf("state = %s, want accepted on resubmit", state)
	}
}

func TestKeyStore_ListByState(t *testing.T) {
	ks := NewKeyStore(AcceptManual)
	ks.SubmitKey("peel-01", "U1", "X1", "")
	ks.SubmitKey("peel-02", "U2", "X2", "")
	ks.SubmitKey("peel-03", "U3", "X3", "")
	ks.AcceptKey("peel-02", "admin")

	pending := ks.ListByState(KeyPending)
	if len(pending) != 2 {
		t.Errorf("pending count = %d, want 2", len(pending))
	}

	accepted := ks.ListByState(KeyAccepted)
	if len(accepted) != 1 {
		t.Errorf("accepted count = %d, want 1", len(accepted))
	}
}

func TestKeyStore_GetRecord(t *testing.T) {
	ks := NewKeyStore(AcceptAutoAll)
	ks.SubmitKey("peel-01", "UABC", "XABC", "")

	record, ok := ks.GetRecord("peel-01")
	if !ok {
		t.Fatal("record not found")
	}
	if record.PeelID != "peel-01" {
		t.Errorf("peel ID = %s, want peel-01", record.PeelID)
	}
	if record.PublicKey != "UABC" {
		t.Errorf("public key = %s, want UABC", record.PublicKey)
	}
	if record.State != KeyAccepted {
		t.Errorf("state = %s, want accepted", record.State)
	}

	// Modifying the returned record should not affect the store.
	record.State = KeyRejected
	record2, _ := ks.GetRecord("peel-01")
	if record2.State != KeyAccepted {
		t.Error("store record was modified through returned copy")
	}
}

func TestKeyStore_GetRecord_NotFound(t *testing.T) {
	ks := NewKeyStore(AcceptManual)
	_, ok := ks.GetRecord("nonexistent")
	if ok {
		t.Error("expected not found")
	}
}

func TestKeyStore_DeleteKey(t *testing.T) {
	ks := NewKeyStore(AcceptAutoAll)
	ks.SubmitKey("peel-01", "UABC", "XABC", "")

	ks.DeleteKey("peel-01")

	_, ok := ks.GetRecord("peel-01")
	if ok {
		t.Error("record should be deleted")
	}
}

func TestKeyStore_RemoveTrustedAccount(t *testing.T) {
	accKP, _ := GenerateKeyBundle(RoleAccount)
	userKP, _ := GenerateKeyBundle(RoleUser)
	token, _ := CreateUserJWT(userKP, accKP, UserJWTOptions{Name: "test"})

	ks := NewKeyStore(AcceptAutoTrusted)
	ks.AddTrustedAccount(accKP.PublicKey)
	ks.RemoveTrustedAccount(accKP.PublicKey)

	state, _ := ks.SubmitKey("peel-01", userKP.PublicKey, "X", token)
	if state != KeyPending {
		t.Errorf("state = %s, want pending after removing trusted account", state)
	}
}
