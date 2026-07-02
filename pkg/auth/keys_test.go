package auth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nats-io/nkeys"
)

func TestGenerateKeyBundle_AllRoles(t *testing.T) {
	roles := []KeyRole{RoleOperator, RoleAccount, RoleUser}
	for _, role := range roles {
		t.Run(role.String(), func(t *testing.T) {
			kb, err := GenerateKeyBundle(role)
			if err != nil {
				t.Fatalf("GenerateKeyBundle(%s): %v", role, err)
			}
			if kb.Role != role {
				t.Errorf("role = %s, want %s", kb.Role, role)
			}
			if kb.PublicKey == "" {
				t.Error("public key is empty")
			}
			if len(kb.Seed) == 0 {
				t.Error("seed is empty")
			}
			if kb.KeyPair == nil {
				t.Error("key pair is nil")
			}

			// Validate the public key matches the role.
			if err := ValidatePublicKey(kb.PublicKey, role); err != nil {
				t.Errorf("ValidatePublicKey: %v", err)
			}
		})
	}
}

func TestGenerateKeyBundle_InvalidRole(t *testing.T) {
	_, err := GenerateKeyBundle(KeyRole(99))
	if err == nil {
		t.Error("expected error for invalid role")
	}
}

func TestLoadKeyBundle_RoundTrip(t *testing.T) {
	roles := []KeyRole{RoleOperator, RoleAccount, RoleUser}
	for _, role := range roles {
		t.Run(role.String(), func(t *testing.T) {
			original, err := GenerateKeyBundle(role)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}

			loaded, err := LoadKeyBundle(role, original.Seed)
			if err != nil {
				t.Fatalf("load: %v", err)
			}

			if loaded.PublicKey != original.PublicKey {
				t.Errorf("public keys differ: got %s, want %s", loaded.PublicKey, original.PublicKey)
			}
		})
	}
}

func TestSaveSeedToFile_AndLoadBack(t *testing.T) {
	kb, err := GenerateKeyBundle(RoleUser)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "user.seed")

	if err := kb.SaveSeedToFile(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Check permissions.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("permissions = %o, want 0600", info.Mode().Perm())
	}

	loaded, err := LoadKeyBundleFromFile(RoleUser, path)
	if err != nil {
		t.Fatalf("load from file: %v", err)
	}
	if loaded.PublicKey != kb.PublicKey {
		t.Errorf("public keys differ after file round-trip")
	}
}

func TestDeriveCurveKeyPair(t *testing.T) {
	kb, err := GenerateKeyBundle(RoleUser)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	ckp, err := kb.DeriveCurveKeyPair()
	if err != nil {
		t.Fatalf("derive curve key pair: %v", err)
	}

	pub, err := ckp.PublicKey()
	if err != nil {
		t.Fatalf("curve public key: %v", err)
	}

	// Curve public keys start with 'X'.
	if !nkeys.IsValidPublicCurveKey(pub) {
		t.Errorf("derived key %s is not a valid curve public key", pub)
	}

	// Deriving again should produce the same key.
	pub2, err := kb.CurvePublicKey()
	if err != nil {
		t.Fatalf("curve public key via helper: %v", err)
	}
	if pub != pub2 {
		t.Errorf("curve keys not deterministic: %s != %s", pub, pub2)
	}
}

func TestPublicKeyFromSeed(t *testing.T) {
	kb, err := GenerateKeyBundle(RoleAccount)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	pub, err := PublicKeyFromSeed(kb.Seed)
	if err != nil {
		t.Fatalf("public key from seed: %v", err)
	}
	if pub != kb.PublicKey {
		t.Errorf("public key mismatch: got %s, want %s", pub, kb.PublicKey)
	}
}

func TestValidatePublicKey(t *testing.T) {
	tests := []struct {
		name    string
		role    KeyRole
		genRole KeyRole
		wantErr bool
	}{
		{"operator-matches", RoleOperator, RoleOperator, false},
		{"account-matches", RoleAccount, RoleAccount, false},
		{"user-matches", RoleUser, RoleUser, false},
		{"operator-as-user", RoleUser, RoleOperator, true},
		{"user-as-account", RoleAccount, RoleUser, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kb, err := GenerateKeyBundle(tt.genRole)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			err = ValidatePublicKey(kb.PublicKey, tt.role)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePublicKey() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestKeyRoleString(t *testing.T) {
	tests := []struct {
		role KeyRole
		want string
	}{
		{RoleOperator, "operator"},
		{RoleAccount, "account"},
		{RoleUser, "user"},
		{KeyRole(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.role.String(); got != tt.want {
			t.Errorf("KeyRole(%d).String() = %s, want %s", tt.role, got, tt.want)
		}
	}
}

func TestLoadKeyBundle_InvalidSeed(t *testing.T) {
	_, err := LoadKeyBundle(RoleUser, []byte("garbage"))
	if err == nil {
		t.Error("expected error for invalid seed")
	}
}

func TestLoadKeyBundleFromFile_NotFound(t *testing.T) {
	_, err := LoadKeyBundleFromFile(RoleUser, "/nonexistent/path")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestSignData(t *testing.T) {
	kb, err := GenerateKeyBundle(RoleUser)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	data := []byte("test message")
	sig, err := kb.KeyPair.Sign(data)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Verify using the public key.
	if err := kb.KeyPair.Verify(data, sig); err != nil {
		t.Errorf("verify: %v", err)
	}

	// Verify tampered data fails.
	tampered := []byte("tampered message")
	if err := kb.KeyPair.Verify(tampered, sig); err == nil {
		t.Error("expected verify to fail for tampered data")
	}
}
