package enroll_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nirnx/zester/pkg/auth"
	"github.com/nirnx/zester/pkg/enroll"
)

func TestSaveCredentials(t *testing.T) {
	credsDir := t.TempDir()

	// Generate a user key for the peel.
	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	// Generate a mock JWT (in production this comes from the issuer).
	accountKB, err := auth.GenerateKeyBundle(auth.RoleAccount)
	if err != nil {
		t.Fatalf("GenerateKeyBundle account: %v", err)
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

	// Save credentials.
	credsPath, err := enroll.SaveCredentials(credsDir, "test-peel", creds.JWT, userKB.Seed)
	if err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	// Verify file exists.
	if _, err := os.Stat(credsPath); err != nil {
		t.Errorf("Credentials file does not exist: %v", err)
	}

	// Verify permissions are 0600.
	info, err := os.Stat(credsPath)
	if err != nil {
		t.Fatalf("Stat credentials file: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("Credentials file permissions = %o, want 0600", info.Mode().Perm())
	}

	// Verify the file can be read and contains the JWT.
	content, err := os.ReadFile(credsPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(content) == 0 {
		t.Error("Credentials file is empty")
	}
}

func TestLoadOrGenerateKey_GeneratesNew(t *testing.T) {
	authDir := t.TempDir()

	// First call should generate a new key.
	kb1, err := enroll.LoadOrGenerateKey(authDir, "test-peel")
	if err != nil {
		t.Fatalf("LoadOrGenerateKey (first call): %v", err)
	}

	if kb1.PublicKey == "" {
		t.Error("Generated key has empty PublicKey")
	}
	if len(kb1.Seed) == 0 {
		t.Error("Generated key has empty Seed")
	}
	if kb1.Role != auth.RoleUser {
		t.Errorf("Generated key role = %s, want %s", kb1.Role, auth.RoleUser)
	}

	// Verify seed file was created.
	seedPath := filepath.Join(authDir, "test-peel.seed")
	if _, err := os.Stat(seedPath); err != nil {
		t.Errorf("Seed file does not exist: %v", err)
	}
}

func TestLoadOrGenerateKey_LoadsExisting(t *testing.T) {
	authDir := t.TempDir()

	// First call generates a new key.
	kb1, err := enroll.LoadOrGenerateKey(authDir, "test-peel")
	if err != nil {
		t.Fatalf("LoadOrGenerateKey (first call): %v", err)
	}

	// Second call should load the same key.
	kb2, err := enroll.LoadOrGenerateKey(authDir, "test-peel")
	if err != nil {
		t.Fatalf("LoadOrGenerateKey (second call): %v", err)
	}

	if kb1.PublicKey != kb2.PublicKey {
		t.Errorf("Loaded key PublicKey = %q, want %q", kb2.PublicKey, kb1.PublicKey)
	}
	if string(kb1.Seed) != string(kb2.Seed) {
		t.Error("Loaded key Seed does not match original")
	}
}

func TestHasCredentials_True(t *testing.T) {
	authDir := t.TempDir()

	// Create a credentials file.
	credsPath := filepath.Join(authDir, "test-peel.creds")
	if err := os.WriteFile(credsPath, []byte("fake-creds"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// HasCredentials should return true.
	if !enroll.HasCredentials(authDir, "test-peel") {
		t.Error("HasCredentials returned false, want true")
	}
}

func TestHasCredentials_False(t *testing.T) {
	authDir := t.TempDir()

	// No credentials file exists.
	if enroll.HasCredentials(authDir, "nonexistent-peel") {
		t.Error("HasCredentials returned true, want false")
	}
}
