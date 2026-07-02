package enroll_test

import (
	"encoding/base64"
	"testing"

	"github.com/ptorbus/zester/pkg/auth"
	"github.com/ptorbus/zester/pkg/enroll"
)

func TestVerifyEnrollSignature_Valid(t *testing.T) {
	// Generate a real user key pair.
	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	// Derive the curve public key.
	curveKey, err := auth.CurvePublicKeyFromSeed(kb.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	// Create a challenge.
	challenge := []byte("test-challenge-32-bytes-long!!!!!")

	// Sign the challenge with the curve key bound.
	signature, err := enroll.SignChallenge(kb.Seed, challenge, curveKey)
	if err != nil {
		t.Fatalf("SignChallenge: %v", err)
	}

	// Verify the signature.
	err = enroll.VerifyEnrollSignature(kb.PublicKey, challenge, signature, curveKey)
	if err != nil {
		t.Errorf("VerifyEnrollSignature failed: %v", err)
	}
}

func TestVerifyEnrollSignature_WrongPublicKey(t *testing.T) {
	// Generate two key pairs.
	kb1, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle kb1: %v", err)
	}
	kb2, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle kb2: %v", err)
	}

	curveKey, err := auth.CurvePublicKeyFromSeed(kb1.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	challenge := []byte("test-challenge-32-bytes-long!!!!!")

	// Sign with kb1.
	signature, err := enroll.SignChallenge(kb1.Seed, challenge, curveKey)
	if err != nil {
		t.Fatalf("SignChallenge: %v", err)
	}

	// Attempt to verify with kb2's public key (wrong key).
	err = enroll.VerifyEnrollSignature(kb2.PublicKey, challenge, signature, curveKey)
	if err == nil {
		t.Error("VerifyEnrollSignature with wrong public key should fail, got nil error")
	}
}

func TestVerifyEnrollSignature_TamperedChallenge(t *testing.T) {
	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	curveKey, err := auth.CurvePublicKeyFromSeed(kb.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	challenge := []byte("test-challenge-32-bytes-long!!!!!")

	signature, err := enroll.SignChallenge(kb.Seed, challenge, curveKey)
	if err != nil {
		t.Fatalf("SignChallenge: %v", err)
	}

	// Tamper with the challenge.
	tamperedChallenge := []byte("TAMPERED-challenge-32-bytes-long!!")

	// Verification should fail.
	err = enroll.VerifyEnrollSignature(kb.PublicKey, tamperedChallenge, signature, curveKey)
	if err == nil {
		t.Error("VerifyEnrollSignature with tampered challenge should fail, got nil error")
	}
}

func TestVerifyEnrollSignature_SwappedCurveKey(t *testing.T) {
	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	// Generate the correct curve key.
	curveKey1, err := auth.CurvePublicKeyFromSeed(kb.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed 1: %v", err)
	}

	// Generate a different curve key (attacker's).
	kb2, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle 2: %v", err)
	}
	curveKey2, err := auth.CurvePublicKeyFromSeed(kb2.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed 2: %v", err)
	}

	challenge := []byte("test-challenge-32-bytes-long!!!!!")

	// Sign with the correct curve key.
	signature, err := enroll.SignChallenge(kb.Seed, challenge, curveKey1)
	if err != nil {
		t.Fatalf("SignChallenge: %v", err)
	}

	// Attempt to verify with a swapped curve key (this is the security fix!).
	err = enroll.VerifyEnrollSignature(kb.PublicKey, challenge, signature, curveKey2)
	if err == nil {
		t.Error("VerifyEnrollSignature with swapped curve key should fail, got nil error")
	}
}

func TestSignChallenge_ProducesValidSignature(t *testing.T) {
	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	curveKey, err := auth.CurvePublicKeyFromSeed(kb.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	challenge := []byte("test-challenge-32-bytes-long!!!!!")

	signature, err := enroll.SignChallenge(kb.Seed, challenge, curveKey)
	if err != nil {
		t.Fatalf("SignChallenge: %v", err)
	}

	if len(signature) == 0 {
		t.Error("SignChallenge returned empty signature")
	}

	// Verify it round-trips.
	err = enroll.VerifyEnrollSignature(kb.PublicKey, challenge, signature, curveKey)
	if err != nil {
		t.Errorf("Round-trip verification failed: %v", err)
	}
}

func TestVerifyCredsSignature_Valid(t *testing.T) {
	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	enrollmentID := "enr-test123"

	// Sign the enrollment ID.
	sigB64, err := enroll.SignEnrollmentID(kb.Seed, enrollmentID)
	if err != nil {
		t.Fatalf("SignEnrollmentID: %v", err)
	}

	// Construct the auth header.
	authHeader := "Nkey " + kb.PublicKey + ":" + sigB64

	// Verify.
	err = enroll.VerifyCredsSignature(authHeader, enrollmentID, kb.PublicKey)
	if err != nil {
		t.Errorf("VerifyCredsSignature failed: %v", err)
	}
}

func TestVerifyCredsSignature_WrongPublicKey(t *testing.T) {
	kb1, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle kb1: %v", err)
	}
	kb2, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle kb2: %v", err)
	}

	enrollmentID := "enr-test123"

	sigB64, err := enroll.SignEnrollmentID(kb1.Seed, enrollmentID)
	if err != nil {
		t.Fatalf("SignEnrollmentID: %v", err)
	}

	// Send kb1's signature but claim kb2 is the expected key.
	authHeader := "Nkey " + kb1.PublicKey + ":" + sigB64

	err = enroll.VerifyCredsSignature(authHeader, enrollmentID, kb2.PublicKey)
	if err == nil {
		t.Error("VerifyCredsSignature with wrong expected key should fail, got nil error")
	}
}

func TestVerifyCredsSignature_InvalidSignature(t *testing.T) {
	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	enrollmentID := "enr-test123"

	// Create an invalid signature.
	invalidSig := base64.RawURLEncoding.EncodeToString([]byte("invalid-signature-data"))
	authHeader := "Nkey " + kb.PublicKey + ":" + invalidSig

	err = enroll.VerifyCredsSignature(authHeader, enrollmentID, kb.PublicKey)
	if err == nil {
		t.Error("VerifyCredsSignature with invalid signature should fail, got nil error")
	}
}

func TestVerifyCredsSignature_MalformedHeader(t *testing.T) {
	tests := []struct {
		name       string
		authHeader string
	}{
		{"missing Nkey prefix", "Bearer token"},
		{"missing colon", "Nkey UABC123"},
		{"empty", ""},
		{"only Nkey", "Nkey"},
		{"extra colons", "Nkey UABC123:sig1:sig2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := enroll.VerifyCredsSignature(tt.authHeader, "enr-test", "UABC123")
			if err == nil {
				t.Errorf("VerifyCredsSignature with malformed header %q should fail, got nil error", tt.authHeader)
			}
		})
	}
}

func TestValidatePeelID(t *testing.T) {
	tests := []struct {
		name    string
		peelID  string
		wantErr bool
	}{
		{"valid simple", "web-01", false},
		{"valid with underscores", "web_server_01", false},
		{"valid with hyphens", "web-server-01", false},
		{"valid alphanumeric", "webserver123", false},
		{"valid two chars", "w1", false},
		{"empty", "", true},
		{"too long", string(make([]byte, 256)), true},
		{"starts with hyphen", "-web01", true},
		{"ends with hyphen", "web01-", true},
		{"starts with underscore", "_web01", true},
		{"ends with underscore", "web01_", true},
		{"special chars", "web@01", true},
		{"spaces", "web 01", true},
		{"single char", "w", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := enroll.ValidatePeelID(tt.peelID)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePeelID(%q) error = %v, wantErr %v", tt.peelID, err, tt.wantErr)
			}
		})
	}
}

func TestValidatePublicKey(t *testing.T) {
	// Generate a valid user key.
	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle user: %v", err)
	}

	// Generate a non-user key (account).
	accountKB, err := auth.GenerateKeyBundle(auth.RoleAccount)
	if err != nil {
		t.Fatalf("GenerateKeyBundle account: %v", err)
	}

	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"valid user key", userKB.PublicKey, false},
		{"invalid account key", accountKB.PublicKey, true},
		{"empty", "", true},
		{"malformed", "invalid-key", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := enroll.ValidatePublicKey(tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePublicKey(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
		})
	}
}

func TestValidateCurvePublicKey(t *testing.T) {
	// Generate a valid curve key.
	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}
	validCurveKey, err := auth.CurvePublicKeyFromSeed(kb.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"valid X prefix", validCurveKey, false},
		{"invalid U prefix", kb.PublicKey, true},
		{"empty", "", true},
		{"malformed", "invalid-key", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := enroll.ValidateCurvePublicKey(tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCurvePublicKey(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
		})
	}
}

func TestSignEnrollmentID_RoundTrip(t *testing.T) {
	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	enrollmentID := "enr-roundtrip-test"

	sigB64, err := enroll.SignEnrollmentID(kb.Seed, enrollmentID)
	if err != nil {
		t.Fatalf("SignEnrollmentID: %v", err)
	}

	if sigB64 == "" {
		t.Error("SignEnrollmentID returned empty signature")
	}

	// Verify via VerifyCredsSignature.
	authHeader := "Nkey " + kb.PublicKey + ":" + sigB64
	err = enroll.VerifyCredsSignature(authHeader, enrollmentID, kb.PublicKey)
	if err != nil {
		t.Errorf("Round-trip verification failed: %v", err)
	}
}
