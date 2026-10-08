package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
)

func TestAccountJWTWithRevocations_RevokesAndKeepsClaims(t *testing.T) {
	h, err := GenerateHierarchy(HierarchyOptions{AccountName: "acct-x"})
	if err != nil {
		t.Fatalf("GenerateHierarchy: %v", err)
	}
	peel, err := GenerateKeyBundle(RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	issuedAt := time.Now().Add(-time.Hour)
	revokedAt := time.Now().Add(-time.Minute)

	token, err := AccountJWTWithRevocations(h.AccountJWT, h.OperatorSigning, map[string]time.Time{peel.PublicKey: revokedAt})
	if err != nil {
		t.Fatalf("AccountJWTWithRevocations: %v", err)
	}
	ac, err := jwt.DecodeAccountClaims(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ac.Subject != h.Account.PublicKey {
		t.Errorf("subject changed: %s != %s", ac.Subject, h.Account.PublicKey)
	}
	if ac.Issuer != h.OperatorSigning.PublicKey {
		t.Errorf("issuer = %s, want operator signing key %s", ac.Issuer, h.OperatorSigning.PublicKey)
	}
	if ac.Name != "acct-x" {
		t.Errorf("name not carried over: %q", ac.Name)
	}
	if ac.Limits.JetStreamLimits.DiskStorage != -1 {
		t.Errorf("JetStream limits not carried over: %+v", ac.Limits.JetStreamLimits)
	}
	if !ac.Revocations.IsRevoked(peel.PublicKey, issuedAt) {
		t.Errorf("JWT issued before the revocation time must be revoked")
	}
	if ac.Revocations.IsRevoked(peel.PublicKey, time.Now()) {
		t.Errorf("a JWT issued AFTER the revocation time (re-enrolled peel) must stay valid")
	}
	other, _ := GenerateKeyBundle(RoleUser)
	if ac.Revocations.IsRevoked(other.PublicKey, issuedAt) {
		t.Errorf("unrelated user must not be revoked")
	}

	// The operator JWT trusts the signing key, so the server will accept the
	// re-signed account: the issuer must be in the operator's signing keys.
	if err := ValidateJWTChain(h.OperatorJWT, token, string(mustUserJWT(t, h, peel))); err != nil {
		t.Errorf("re-signed account JWT not trusted by the operator chain: %v", err)
	}
}

func TestAccountJWTWithRevocations_EmptyClearsList(t *testing.T) {
	h, err := GenerateHierarchy(HierarchyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	peel, _ := GenerateKeyBundle(RoleUser)
	withRev, err := AccountJWTWithRevocations(h.AccountJWT, h.OperatorSigning, map[string]time.Time{peel.PublicKey: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	// Re-signing from a base that already has revocations REPLACES the list.
	cleared, err := AccountJWTWithRevocations(withRev, h.OperatorSigning, nil)
	if err != nil {
		t.Fatal(err)
	}
	ac, _ := jwt.DecodeAccountClaims(cleared)
	if len(ac.Revocations) != 0 {
		t.Errorf("expected empty revocation list, got %v", ac.Revocations)
	}
}

func TestAccountJWTWithRevocations_Rejections(t *testing.T) {
	h, err := GenerateHierarchy(HierarchyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AccountJWTWithRevocations(h.AccountJWT, h.Account, nil); err == nil || !strings.Contains(err.Error(), "operator") {
		t.Errorf("account key must be refused as signer, got %v", err)
	}
	if _, err := AccountJWTWithRevocations(h.AccountJWT, nil, nil); err == nil {
		t.Errorf("nil signer must be refused")
	}
	if _, err := AccountJWTWithRevocations("not-a-jwt", h.OperatorSigning, nil); err == nil {
		t.Errorf("garbage base JWT must be refused")
	}
	if _, err := AccountJWTWithRevocations(h.AccountJWT, h.OperatorSigning, map[string]time.Time{"ABCDEF": time.Now()}); err == nil {
		t.Errorf("non-user public key must be refused")
	}
}

func TestParseClaimsUpdateReply_Outcomes(t *testing.T) {
	r, err := ParseClaimsUpdateReply([]byte(`{"server":{"name":"n1"},"data":{"account":"A","code":200,"message":"jwt updated"}}`))
	if err != nil || r.Outcome != ClaimsUpdateApplied || r.Server != "n1" {
		t.Errorf("applied reply = %+v, %v", r, err)
	}
	r, err = ParseClaimsUpdateReply([]byte(`{"server":{"name":"n3"},"data":{"account":"A","code":200,"message":"jwt update skipped"}}`))
	if err != nil || r.Outcome != ClaimsUpdateSkipped || r.Server != "n3" {
		t.Errorf("skipped reply = %+v, %v", r, err)
	}
	if AccountConnectEventSubject("ACCT") != "$SYS.ACCOUNT.ACCT.CONNECT" {
		t.Errorf("connect event subject = %q", AccountConnectEventSubject("ACCT"))
	}
}

func TestAccountClaimsUpdateSubject(t *testing.T) {
	got := AccountClaimsUpdateSubject("ACCT")
	if got != "$SYS.REQ.ACCOUNT.ACCT.CLAIMS.UPDATE" {
		t.Errorf("subject = %q", got)
	}
}

func TestParseClaimsUpdateResponse(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"updated", `{"server":{"name":"n1"},"data":{"account":"A","code":200,"message":"jwt updated"}}`, ""},
		{"skipped", `{"server":{"name":"n2"},"data":{"account":"A","code":200,"message":"jwt update skipped"}}`, ""},
		{"unapplied", `{"server":{},"data":{"account":"A","code":304,"message":"jwt update ignored"}}`, "304"},
		{"error", `{"server":{},"error":{"code":500,"description":"jwt update resulted in error: bad sig"}}`, "bad sig"},
		{"empty", `{"server":{}}`, "neither data nor error"},
		{"garbage", `-ERR not json`, "decode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ParseClaimsUpdateResponse([]byte(tc.body))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// mustUserJWT mints a peel user JWT under the hierarchy's account, as the
// enrollment issuer does.
func mustUserJWT(t *testing.T, h *Hierarchy, user *KeyBundle) string {
	t.Helper()
	token, err := CreateUserJWTForPublicKey(user.PublicKey, h.Account, PeelUserJWTOptions("peel-1", h.Account.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	return token
}
