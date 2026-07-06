package enroll_test

import (
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/auth"
	"github.com/nirnx/zester/pkg/enroll"
)

func TestNewCredentialIssuer_RequiresAccountKey(t *testing.T) {
	// Attempt to create issuer with nil key bundle.
	_, err := enroll.NewCredentialIssuer(enroll.CredentialIssuerConfig{
		AccountKP: nil,
	})
	if err == nil {
		t.Error("NewCredentialIssuer with nil AccountKP should fail, got nil error")
	}
}

func TestNewCredentialIssuer_RejectsNonAccountKey(t *testing.T) {
	// Generate a user key (not an account key).
	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	_, err = enroll.NewCredentialIssuer(enroll.CredentialIssuerConfig{
		AccountKP: userKB,
	})
	if err == nil {
		t.Error("NewCredentialIssuer with user key should fail, got nil error")
	}
}

func TestCredentialIssuerIssue(t *testing.T) {
	// Generate an account key.
	accountKB, err := auth.GenerateKeyBundle(auth.RoleAccount)
	if err != nil {
		t.Fatalf("GenerateKeyBundle account: %v", err)
	}

	// Generate a user key for the peel.
	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle user: %v", err)
	}

	issuer, err := enroll.NewCredentialIssuer(enroll.CredentialIssuerConfig{
		AccountKP: accountKB,
		JWTExpiry: 180 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewCredentialIssuer: %v", err)
	}

	// Issue credentials for the peel.
	creds, err := issuer.Issue("test-peel", userKB.PublicKey)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Verify fields.
	if creds.JWT == "" {
		t.Error("Issued JWT is empty")
	}
	if creds.ExpiresAt.IsZero() {
		t.Error("Issued ExpiresAt is zero")
	}

	// Verify expiry is roughly correct (within 1 minute tolerance).
	expectedExpiry := time.Now().Add(180 * 24 * time.Hour)
	diff := creds.ExpiresAt.Sub(expectedExpiry)
	if diff < -time.Minute || diff > time.Minute {
		t.Errorf("ExpiresAt = %v, expected near %v (diff: %v)", creds.ExpiresAt, expectedExpiry, diff)
	}
}

func TestCredentialIssuerDefaultExpiry(t *testing.T) {
	accountKB, err := auth.GenerateKeyBundle(auth.RoleAccount)
	if err != nil {
		t.Fatalf("GenerateKeyBundle account: %v", err)
	}

	// Create issuer with zero JWTExpiry (should use default).
	issuer, err := enroll.NewCredentialIssuer(enroll.CredentialIssuerConfig{
		AccountKP: accountKB,
		JWTExpiry: 0, // Should default to 180 days.
	})
	if err != nil {
		t.Fatalf("NewCredentialIssuer: %v", err)
	}

	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle user: %v", err)
	}

	creds, err := issuer.Issue("test-peel", userKB.PublicKey)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Verify it used the default (180 days).
	expectedExpiry := time.Now().Add(enroll.DefaultJWTExpiry)
	diff := creds.ExpiresAt.Sub(expectedExpiry)
	if diff < -time.Minute || diff > time.Minute {
		t.Errorf("ExpiresAt with default = %v, expected near %v (diff: %v)", creds.ExpiresAt, expectedExpiry, diff)
	}
}

func TestEncodeDecodeJWTForTransport(t *testing.T) {
	accountKB, err := auth.GenerateKeyBundle(auth.RoleAccount)
	if err != nil {
		t.Fatalf("GenerateKeyBundle account: %v", err)
	}

	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle user: %v", err)
	}

	issuer, err := enroll.NewCredentialIssuer(enroll.CredentialIssuerConfig{
		AccountKP: accountKB,
	})
	if err != nil {
		t.Fatalf("NewCredentialIssuer: %v", err)
	}

	creds, err := issuer.Issue("test-peel", userKB.PublicKey)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Encode for transport.
	encoded := enroll.EncodeJWTForTransport(creds.JWT)
	if encoded == "" {
		t.Error("EncodeJWTForTransport returned empty string")
	}

	// Decode and verify round-trip.
	decoded, err := enroll.DecodeJWTFromTransport(encoded)
	if err != nil {
		t.Fatalf("DecodeJWTFromTransport: %v", err)
	}

	if decoded != creds.JWT {
		t.Errorf("Round-trip failed: got %q, want %q", decoded, creds.JWT)
	}
}

func TestDecodeJWTFromTransport_InvalidBase64(t *testing.T) {
	_, err := enroll.DecodeJWTFromTransport("not-valid-base64!!!")
	if err == nil {
		t.Error("DecodeJWTFromTransport with invalid base64 should fail, got nil error")
	}
}
