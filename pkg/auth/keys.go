package auth

import (
	"fmt"
	"os"

	"github.com/nats-io/nkeys"
)

// KeyRole represents the role of an nkey in the trust hierarchy.
type KeyRole int

const (
	RoleOperator KeyRole = iota
	RoleAccount
	RoleUser
)

func (r KeyRole) String() string {
	switch r {
	case RoleOperator:
		return "operator"
	case RoleAccount:
		return "account"
	case RoleUser:
		return "user"
	default:
		return "unknown"
	}
}

// KeyBundle holds a key pair along with its role and extracted identifiers.
type KeyBundle struct {
	Role      KeyRole
	KeyPair   nkeys.KeyPair
	PublicKey string
	Seed      []byte
}

// GenerateKeyBundle creates a new nkey pair for the given role.
func GenerateKeyBundle(role KeyRole) (*KeyBundle, error) {
	var kp nkeys.KeyPair
	var err error

	switch role {
	case RoleOperator:
		kp, err = nkeys.CreateOperator()
	case RoleAccount:
		kp, err = nkeys.CreateAccount()
	case RoleUser:
		kp, err = nkeys.CreateUser()
	default:
		return nil, fmt.Errorf("unknown key role: %d", role)
	}
	if err != nil {
		return nil, fmt.Errorf("create %s key pair: %w", role, err)
	}

	return bundleFromKeyPair(role, kp)
}

// LoadKeyBundle reconstructs a KeyBundle from a stored seed.
func LoadKeyBundle(role KeyRole, seed []byte) (*KeyBundle, error) {
	kp, err := nkeys.FromSeed(seed)
	if err != nil {
		return nil, fmt.Errorf("load key pair from seed: %w", err)
	}
	return bundleFromKeyPair(role, kp)
}

// LoadKeyBundleFromFile reads a seed file and reconstructs the KeyBundle.
func LoadKeyBundleFromFile(role KeyRole, path string) (*KeyBundle, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read seed file %s: %w", path, err)
	}
	// Support decorated nkey format (as found in .creds files).
	kp, err := nkeys.ParseDecoratedNKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse seed from %s: %w", path, err)
	}
	return bundleFromKeyPair(role, kp)
}

// SaveSeedToFile writes the seed to a file with restricted permissions.
func (kb *KeyBundle) SaveSeedToFile(path string) error {
	return os.WriteFile(path, kb.Seed, 0600)
}

// DeriveCurveKeyPair derives an X25519 curve key pair from this bundle's
// Ed25519 seed. This allows using the same identity seed for NaCl box
// encryption/decryption while keeping the signing key pair separate.
func (kb *KeyBundle) DeriveCurveKeyPair() (nkeys.KeyPair, error) {
	_, rawSeed, err := nkeys.DecodeSeed(kb.Seed)
	if err != nil {
		return nil, fmt.Errorf("decode seed for curve derivation: %w", err)
	}
	curveSeed, err := nkeys.EncodeSeed(nkeys.PrefixByteCurve, rawSeed)
	if err != nil {
		return nil, fmt.Errorf("encode curve seed: %w", err)
	}
	ckp, err := nkeys.FromCurveSeed(curveSeed)
	if err != nil {
		return nil, fmt.Errorf("create curve key pair: %w", err)
	}
	return ckp, nil
}

// CurvePublicKey returns the X25519 public key derived from this bundle's seed.
func (kb *KeyBundle) CurvePublicKey() (string, error) {
	ckp, err := kb.DeriveCurveKeyPair()
	if err != nil {
		return "", err
	}
	return ckp.PublicKey()
}

// PublicKeyFromSeed extracts the public key from a raw seed without
// constructing a full bundle.
func PublicKeyFromSeed(seed []byte) (string, error) {
	kp, err := nkeys.FromSeed(seed)
	if err != nil {
		return "", fmt.Errorf("key pair from seed: %w", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return "", fmt.Errorf("extract public key: %w", err)
	}
	return pub, nil
}

// ValidatePublicKey checks that a public key string is well-formed and
// matches the expected role prefix.
func ValidatePublicKey(pub string, role KeyRole) error {
	switch role {
	case RoleOperator:
		if !nkeys.IsValidPublicOperatorKey(pub) {
			return fmt.Errorf("invalid operator public key: %s", pub)
		}
	case RoleAccount:
		if !nkeys.IsValidPublicAccountKey(pub) {
			return fmt.Errorf("invalid account public key: %s", pub)
		}
	case RoleUser:
		if !nkeys.IsValidPublicUserKey(pub) {
			return fmt.Errorf("invalid user public key: %s", pub)
		}
	default:
		return fmt.Errorf("unknown role for validation: %d", role)
	}
	return nil
}

func bundleFromKeyPair(role KeyRole, kp nkeys.KeyPair) (*KeyBundle, error) {
	pub, err := kp.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("extract public key: %w", err)
	}
	seed, err := kp.Seed()
	if err != nil {
		return nil, fmt.Errorf("extract seed: %w", err)
	}
	return &KeyBundle{
		Role:      role,
		KeyPair:   kp,
		PublicKey: pub,
		Seed:      seed,
	}, nil
}
