package auth

import (
	"bytes"
	"testing"
)

func TestEncryptor_SealAndOpen(t *testing.T) {
	senderKB, _ := GenerateKeyBundle(RoleUser)
	recipientKB, _ := GenerateKeyBundle(RoleUser)

	senderEnc, err := NewEncryptor(senderKB)
	if err != nil {
		t.Fatalf("create sender encryptor: %v", err)
	}
	recipientEnc, err := NewEncryptor(recipientKB)
	if err != nil {
		t.Fatalf("create recipient encryptor: %v", err)
	}

	plaintext := []byte("secret database password")
	encrypted, err := senderEnc.Seal(plaintext, recipientEnc.PublicKey())
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	if bytes.Equal(encrypted, plaintext) {
		t.Error("encrypted data equals plaintext")
	}

	decrypted, err := recipientEnc.Open(encrypted, senderEnc.PublicKey())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("decrypted = %s, want %s", decrypted, plaintext)
	}
}

func TestEncryptor_SealAndOpen_LargeData(t *testing.T) {
	senderKB, _ := GenerateKeyBundle(RoleOperator)
	recipientKB, _ := GenerateKeyBundle(RoleUser)

	senderEnc, _ := NewEncryptor(senderKB)
	recipientEnc, _ := NewEncryptor(recipientKB)

	// 64KB of data.
	plaintext := make([]byte, 64*1024)
	for i := range plaintext {
		plaintext[i] = byte(i % 256)
	}

	encrypted, err := senderEnc.Seal(plaintext, recipientEnc.PublicKey())
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	decrypted, err := recipientEnc.Open(encrypted, senderEnc.PublicKey())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Error("large data round-trip failed")
	}
}

func TestEncryptor_WrongRecipient(t *testing.T) {
	senderKB, _ := GenerateKeyBundle(RoleUser)
	recipientKB, _ := GenerateKeyBundle(RoleUser)
	wrongKB, _ := GenerateKeyBundle(RoleUser)

	senderEnc, _ := NewEncryptor(senderKB)
	recipientEnc, _ := NewEncryptor(recipientKB)
	wrongEnc, _ := NewEncryptor(wrongKB)

	plaintext := []byte("secret")
	encrypted, _ := senderEnc.Seal(plaintext, recipientEnc.PublicKey())

	// Wrong recipient should fail.
	_, err := wrongEnc.Open(encrypted, senderEnc.PublicKey())
	if err == nil {
		t.Error("expected error when decrypting with wrong key")
	}
}

func TestSealSettingsValue(t *testing.T) {
	masterKB, _ := GenerateKeyBundle(RoleOperator)
	peelKB, _ := GenerateKeyBundle(RoleUser)

	masterEnc, _ := NewEncryptor(masterKB)
	peelEnc, _ := NewEncryptor(peelKB)

	tagged, err := masterEnc.SealSettingsValue(
		[]byte("super-secret-password"),
		peelEnc.PublicKey(),
	)
	if err != nil {
		t.Fatalf("seal settings value: %v", err)
	}

	if !IsEncryptedValue(tagged) {
		t.Errorf("tagged value %q is not recognized as encrypted", tagged)
	}

	decrypted, err := peelEnc.OpenSettingsValue(tagged, masterEnc.PublicKey())
	if err != nil {
		t.Fatalf("open settings value: %v", err)
	}

	if string(decrypted) != "super-secret-password" {
		t.Errorf("decrypted = %s, want super-secret-password", decrypted)
	}
}

func TestIsEncryptedValue(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"ENC[nkey,abc123]", true},
		{"ENC[nkey,]", true},
		{"plain text", false},
		{"ENC[other,abc]", false},
		{"ENC[nkey,abc", false},
	}
	for _, tt := range tests {
		if got := IsEncryptedValue(tt.input); got != tt.want {
			t.Errorf("IsEncryptedValue(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestExtractEncryptedPayload(t *testing.T) {
	payload, err := ExtractEncryptedPayload("ENC[nkey,dGVzdA==]")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if payload != "dGVzdA==" {
		t.Errorf("payload = %s, want dGVzdA==", payload)
	}

	_, err = ExtractEncryptedPayload("not encrypted")
	if err == nil {
		t.Error("expected error for non-encrypted value")
	}
}

func TestEncryptSettingsMap(t *testing.T) {
	masterKB, _ := GenerateKeyBundle(RoleOperator)
	peelKB, _ := GenerateKeyBundle(RoleUser)

	masterEnc, _ := NewEncryptor(masterKB)
	peelEnc, _ := NewEncryptor(peelKB)

	settings := map[string]any{
		"db_host":     "localhost",
		"db_password": "secret123",
		"db_port":     5432,
		"api_key":     "key-abc-def",
	}
	sensitiveKeys := []string{"db_password", "api_key"}

	encrypted, err := EncryptSettingsMap(settings, sensitiveKeys, masterEnc, peelEnc.PublicKey())
	if err != nil {
		t.Fatalf("encrypt settings map: %v", err)
	}

	// Non-sensitive keys should be unchanged.
	if encrypted["db_host"] != "localhost" {
		t.Errorf("db_host changed: %v", encrypted["db_host"])
	}
	if encrypted["db_port"] != 5432 {
		t.Errorf("db_port changed: %v", encrypted["db_port"])
	}

	// Sensitive keys should be encrypted.
	if !IsEncryptedValue(encrypted["db_password"].(string)) {
		t.Error("db_password was not encrypted")
	}
	if !IsEncryptedValue(encrypted["api_key"].(string)) {
		t.Error("api_key was not encrypted")
	}

	// Decrypt and verify.
	decrypted, err := DecryptSettingsMap(encrypted, peelEnc, masterEnc.PublicKey())
	if err != nil {
		t.Fatalf("decrypt settings map: %v", err)
	}

	if decrypted["db_password"] != "secret123" {
		t.Errorf("db_password = %v, want secret123", decrypted["db_password"])
	}
	if decrypted["api_key"] != "key-abc-def" {
		t.Errorf("api_key = %v, want key-abc-def", decrypted["api_key"])
	}
	if decrypted["db_host"] != "localhost" {
		t.Errorf("db_host = %v, want localhost", decrypted["db_host"])
	}
}

func TestEncryptSettingsMap_NonStringValues(t *testing.T) {
	masterKB, _ := GenerateKeyBundle(RoleOperator)
	peelKB, _ := GenerateKeyBundle(RoleUser)
	masterEnc, _ := NewEncryptor(masterKB)

	settings := map[string]any{
		"count": 42, // non-string sensitive key should pass through
	}

	result, err := EncryptSettingsMap(settings, []string{"count"}, masterEnc, mustCurvePub(peelKB))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if result["count"] != 42 {
		t.Errorf("non-string value was modified: %v", result["count"])
	}
}

func TestCurvePublicKeyFromSeed(t *testing.T) {
	kb, _ := GenerateKeyBundle(RoleUser)

	curvePub, err := CurvePublicKeyFromSeed(kb.Seed)
	if err != nil {
		t.Fatalf("curve public key from seed: %v", err)
	}

	// Should match what DeriveCurveKeyPair produces.
	expected, _ := kb.CurvePublicKey()
	if curvePub != expected {
		t.Errorf("curve pub = %s, want %s", curvePub, expected)
	}
}

func TestCurvePublicKeyFromSeed_InvalidSeed(t *testing.T) {
	_, err := CurvePublicKeyFromSeed([]byte("garbage"))
	if err == nil {
		t.Error("expected error for invalid seed")
	}
}

func TestNewEncryptorFromCurveKey(t *testing.T) {
	kb, _ := GenerateKeyBundle(RoleUser)
	ckp, _ := kb.DeriveCurveKeyPair()

	enc, err := NewEncryptorFromCurveKey(ckp)
	if err != nil {
		t.Fatalf("create encryptor from curve key: %v", err)
	}
	if enc.PublicKey() == "" {
		t.Error("public key is empty")
	}
}

func TestEncryptor_DeterministicCurveKeys(t *testing.T) {
	// Same seed should always produce the same curve key.
	kb, _ := GenerateKeyBundle(RoleUser)
	enc1, _ := NewEncryptor(kb)
	enc2, _ := NewEncryptor(kb)

	if enc1.PublicKey() != enc2.PublicKey() {
		t.Error("encryptors from same key bundle have different public keys")
	}
}

func mustCurvePub(kb *KeyBundle) string {
	pub, err := kb.CurvePublicKey()
	if err != nil {
		panic(err)
	}
	return pub
}
