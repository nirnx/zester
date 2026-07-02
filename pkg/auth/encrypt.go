package auth

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/nats-io/nkeys"
)

const (
	// EncryptedPrefix marks an encrypted settings value.
	EncryptedPrefix = "ENC[nkey,"
	EncryptedSuffix = "]"
)

// Encryptor provides NaCl box encryption using X25519 curve keys derived
// from Ed25519 nkey seeds. This is used for encrypting sensitive settings
// values that are stored in NATS KV and should only be readable by the
// target peel.
type Encryptor struct {
	curveKP nkeys.KeyPair
	pubKey  string
}

// NewEncryptor creates an encryptor from a key bundle. It derives the
// X25519 curve key pair from the Ed25519 seed.
func NewEncryptor(kb *KeyBundle) (*Encryptor, error) {
	ckp, err := kb.DeriveCurveKeyPair()
	if err != nil {
		return nil, fmt.Errorf("derive curve key pair: %w", err)
	}
	pub, err := ckp.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("get curve public key: %w", err)
	}
	return &Encryptor{
		curveKP: ckp,
		pubKey:  pub,
	}, nil
}

// NewEncryptorFromCurveKey creates an encryptor directly from a curve key pair.
func NewEncryptorFromCurveKey(ckp nkeys.KeyPair) (*Encryptor, error) {
	pub, err := ckp.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("get curve public key: %w", err)
	}
	return &Encryptor{
		curveKP: ckp,
		pubKey:  pub,
	}, nil
}

// PublicKey returns the X25519 public key of this encryptor.
func (e *Encryptor) PublicKey() string {
	return e.pubKey
}

// Seal encrypts plaintext for the given recipient X25519 public key.
// Returns the encrypted data as raw bytes.
func (e *Encryptor) Seal(plaintext []byte, recipientPub string) ([]byte, error) {
	encrypted, err := e.curveKP.Seal(plaintext, recipientPub)
	if err != nil {
		return nil, fmt.Errorf("seal: %w", err)
	}
	return encrypted, nil
}

// Open decrypts ciphertext from the given sender X25519 public key.
func (e *Encryptor) Open(encrypted []byte, senderPub string) ([]byte, error) {
	plaintext, err := e.curveKP.Open(encrypted, senderPub)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	return plaintext, nil
}

// SealSettingsValue encrypts a settings value and returns the tagged
// string suitable for embedding in YAML: ENC[nkey,<base64>]
func (e *Encryptor) SealSettingsValue(plaintext []byte, recipientPub string) (string, error) {
	encrypted, err := e.Seal(plaintext, recipientPub)
	if err != nil {
		return "", err
	}
	encoded := base64.StdEncoding.EncodeToString(encrypted)
	return EncryptedPrefix + encoded + EncryptedSuffix, nil
}

// OpenSettingsValue decrypts a tagged settings value (ENC[nkey,...]).
func (e *Encryptor) OpenSettingsValue(tagged string, senderPub string) ([]byte, error) {
	encoded, err := ExtractEncryptedPayload(tagged)
	if err != nil {
		return nil, err
	}
	encrypted, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("base64 decode encrypted value: %w", err)
	}
	return e.Open(encrypted, senderPub)
}

// IsEncryptedValue checks whether a string is a tagged encrypted value.
func IsEncryptedValue(s string) bool {
	return strings.HasPrefix(s, EncryptedPrefix) && strings.HasSuffix(s, EncryptedSuffix)
}

// ExtractEncryptedPayload extracts the base64 payload from a tagged value.
func ExtractEncryptedPayload(tagged string) (string, error) {
	if !IsEncryptedValue(tagged) {
		return "", fmt.Errorf("not an encrypted value: missing ENC[nkey,...] wrapper")
	}
	inner := tagged[len(EncryptedPrefix) : len(tagged)-len(EncryptedSuffix)]
	return inner, nil
}

// EncryptSettingsMap encrypts specific keys in a settings map. The keys
// listed in sensitiveKeys are dot-separated paths (e.g. "database.password")
// that identify nested values to encrypt for the target peel's curve public key.
func EncryptSettingsMap(
	settings map[string]any,
	sensitiveKeys []string,
	senderEncryptor *Encryptor,
	recipientCurvePub string,
) (map[string]any, error) {
	result := deepCopyMap(settings)
	for _, dotPath := range sensitiveKeys {
		parts := strings.Split(dotPath, ".")
		if err := encryptAtPath(result, parts, senderEncryptor, recipientCurvePub); err != nil {
			return nil, fmt.Errorf("encrypt key %q: %w", dotPath, err)
		}
	}
	return result, nil
}

func encryptAtPath(m map[string]any, parts []string, enc *Encryptor, recipientPub string) error {
	if len(parts) == 1 {
		str, ok := m[parts[0]].(string)
		if !ok {
			return nil
		}
		encrypted, err := enc.SealSettingsValue([]byte(str), recipientPub)
		if err != nil {
			return err
		}
		m[parts[0]] = encrypted
		return nil
	}
	next, ok := m[parts[0]].(map[string]any)
	if !ok {
		return nil
	}
	return encryptAtPath(next, parts[1:], enc, recipientPub)
}

func deepCopyMap(m map[string]any) map[string]any {
	result := make(map[string]any, len(m))
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			result[k] = deepCopyMap(sub)
		} else {
			result[k] = v
		}
	}
	return result
}

// DecryptSettingsMap decrypts all ENC[nkey,...] values in a settings map,
// recursively walking nested maps.
func DecryptSettingsMap(
	settings map[string]any,
	recipientEncryptor *Encryptor,
	senderCurvePub string,
) (map[string]any, error) {
	return decryptMapRecursive(settings, recipientEncryptor, senderCurvePub)
}

func decryptMapRecursive(m map[string]any, enc *Encryptor, senderPub string) (map[string]any, error) {
	result := make(map[string]any, len(m))
	for k, v := range m {
		switch val := v.(type) {
		case string:
			if IsEncryptedValue(val) {
				decrypted, err := enc.OpenSettingsValue(val, senderPub)
				if err != nil {
					return nil, fmt.Errorf("decrypt key %q: %w", k, err)
				}
				result[k] = string(decrypted)
			} else {
				result[k] = val
			}
		case map[string]any:
			sub, err := decryptMapRecursive(val, enc, senderPub)
			if err != nil {
				return nil, err
			}
			result[k] = sub
		default:
			result[k] = v
		}
	}
	return result, nil
}

// CurvePublicKeyFromSeed derives the X25519 curve public key from an
// Ed25519 nkey seed. Useful when the master needs to know a peel's
// encryption public key given only its signing seed.
func CurvePublicKeyFromSeed(seed []byte) (string, error) {
	_, rawSeed, err := nkeys.DecodeSeed(seed)
	if err != nil {
		return "", fmt.Errorf("decode seed: %w", err)
	}
	curveSeed, err := nkeys.EncodeSeed(nkeys.PrefixByteCurve, rawSeed)
	if err != nil {
		return "", fmt.Errorf("encode curve seed: %w", err)
	}
	ckp, err := nkeys.FromCurveSeed(curveSeed)
	if err != nil {
		return "", fmt.Errorf("create curve key pair: %w", err)
	}
	pub, err := ckp.PublicKey()
	if err != nil {
		return "", fmt.Errorf("get curve public key: %w", err)
	}
	return pub, nil
}
