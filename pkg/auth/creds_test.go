package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateCredsFile(t *testing.T) {
	accKP, _ := GenerateKeyBundle(RoleAccount)
	userKP, _ := GenerateKeyBundle(RoleUser)

	token, err := CreateUserJWT(userKP, accKP, UserJWTOptions{Name: "creds-test"})
	if err != nil {
		t.Fatalf("create user JWT: %v", err)
	}

	contents, err := GenerateCredsFile(token, userKP.Seed)
	if err != nil {
		t.Fatalf("generate creds: %v", err)
	}
	if len(contents) == 0 {
		t.Error("creds contents is empty")
	}
}

func TestWriteAndLoadCredsFile(t *testing.T) {
	accKP, _ := GenerateKeyBundle(RoleAccount)
	userKP, _ := GenerateKeyBundle(RoleUser)

	token, err := CreateUserJWT(userKP, accKP, UserJWTOptions{Name: "creds-roundtrip"})
	if err != nil {
		t.Fatalf("create user JWT: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.creds")

	if err := WriteCredsFile(path, token, userKP.Seed); err != nil {
		t.Fatalf("write creds: %v", err)
	}

	// Verify file permissions.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("permissions = %o, want 0600", info.Mode().Perm())
	}

	// Load and verify.
	cf, err := LoadCredsFile(path)
	if err != nil {
		t.Fatalf("load creds: %v", err)
	}
	if cf.JWT != token {
		t.Error("JWT mismatch after round-trip")
	}
	if cf.Path != path {
		t.Errorf("path = %s, want %s", cf.Path, path)
	}

	// Verify the seed produces the same public key.
	pub, err := PublicKeyFromSeed(cf.Seed)
	if err != nil {
		t.Fatalf("public key from loaded seed: %v", err)
	}
	if pub != userKP.PublicKey {
		t.Errorf("public key mismatch: got %s, want %s", pub, userKP.PublicKey)
	}
}

func TestParseCredsData(t *testing.T) {
	accKP, _ := GenerateKeyBundle(RoleAccount)
	userKP, _ := GenerateKeyBundle(RoleUser)

	token, _ := CreateUserJWT(userKP, accKP, UserJWTOptions{Name: "parse-test"})
	contents, _ := GenerateCredsFile(token, userKP.Seed)

	cf, err := ParseCredsData(contents, "inline")
	if err != nil {
		t.Fatalf("parse creds data: %v", err)
	}
	if cf.JWT != token {
		t.Error("JWT mismatch")
	}
	if cf.Path != "inline" {
		t.Errorf("path = %s, want inline", cf.Path)
	}
}

func TestParseCredsData_Invalid(t *testing.T) {
	_, err := ParseCredsData([]byte("not a creds file"), "bad")
	if err == nil {
		t.Error("expected error for invalid creds data")
	}
}

func TestLoadCredsFile_NotFound(t *testing.T) {
	_, err := LoadCredsFile("/nonexistent/file.creds")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestCredsFile_NATSOption(t *testing.T) {
	accKP, _ := GenerateKeyBundle(RoleAccount)
	userKP, _ := GenerateKeyBundle(RoleUser)

	token, _ := CreateUserJWT(userKP, accKP, UserJWTOptions{Name: "nats-opt"})

	dir := t.TempDir()
	path := filepath.Join(dir, "test.creds")
	WriteCredsFile(path, token, userKP.Seed)

	// File-based creds.
	cf, _ := LoadCredsFile(path)
	opt := cf.NATSOption()
	if opt == nil {
		t.Error("NATSOption returned nil for file-based creds")
	}

	// Inline creds (no path).
	inlineCF, _ := ParseCredsData([]byte{}, "")
	// This will fail parsing, so test with valid data and empty path.
	contents, _ := GenerateCredsFile(token, userKP.Seed)
	inlineCF, _ = ParseCredsData(contents, "")
	opt = inlineCF.NATSOption()
	if opt == nil {
		t.Error("NATSOption returned nil for inline creds")
	}
}

func TestNATSOptionFromSeed(t *testing.T) {
	kb, _ := GenerateKeyBundle(RoleUser)
	opt, err := NATSOptionFromSeed(kb.Seed)
	if err != nil {
		t.Fatalf("NATSOptionFromSeed: %v", err)
	}
	if opt == nil {
		t.Error("option is nil")
	}
}

func TestNATSOptionFromSeed_InvalidSeed(t *testing.T) {
	_, err := NATSOptionFromSeed([]byte("garbage"))
	if err == nil {
		t.Error("expected error for invalid seed")
	}
}

func TestBootstrapPeelCreds(t *testing.T) {
	accKP, _ := GenerateKeyBundle(RoleAccount)

	dir := t.TempDir()
	path := filepath.Join(dir, "peel-web-01.creds")

	userKP, userJWT, err := BootstrapPeelCreds(accKP, "web-01", path)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if userKP == nil {
		t.Fatal("user key bundle is nil")
	}
	if userJWT == "" {
		t.Fatal("user JWT is empty")
	}

	// Verify the creds file exists and is valid.
	cf, err := LoadCredsFile(path)
	if err != nil {
		t.Fatalf("load bootstrapped creds: %v", err)
	}
	if cf.JWT != userJWT {
		t.Error("JWT mismatch in bootstrapped creds")
	}

	// Verify the user JWT claims.
	uc, err := DecodeUserJWT(cf.JWT)
	if err != nil {
		t.Fatalf("decode bootstrapped JWT: %v", err)
	}
	if uc.Name != "web-01" {
		t.Errorf("name = %s, want web-01", uc.Name)
	}
}

func TestNATSOptionsForPeel(t *testing.T) {
	opts := NATSOptionsForPeel("/tmp/fake.creds", "web-01")
	if len(opts) == 0 {
		t.Error("expected non-empty options")
	}
}

func TestNATSOptionsForMaster(t *testing.T) {
	opts := NATSOptionsForMaster("/tmp/fake.creds")
	if len(opts) == 0 {
		t.Error("expected non-empty options")
	}
}
