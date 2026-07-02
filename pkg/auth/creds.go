package auth

import (
	"fmt"
	"os"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

// CredsFile represents a NATS credentials file containing a user JWT
// and the corresponding nkey seed.
type CredsFile struct {
	JWT  string
	Seed []byte
	Path string
}

// GenerateCredsFile creates the contents of a .creds file from a user JWT
// and user seed.
func GenerateCredsFile(userJWT string, userSeed []byte) ([]byte, error) {
	contents, err := jwt.FormatUserConfig(userJWT, userSeed)
	if err != nil {
		return nil, fmt.Errorf("format user config: %w", err)
	}
	return contents, nil
}

// WriteCredsFile generates a .creds file and writes it to disk with
// restricted permissions (0600).
func WriteCredsFile(path, userJWT string, userSeed []byte) error {
	contents, err := GenerateCredsFile(userJWT, userSeed)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, contents, 0600); err != nil {
		return fmt.Errorf("write creds file %s: %w", path, err)
	}
	return nil
}

// LoadCredsFile reads a .creds file and extracts the JWT and key pair.
func LoadCredsFile(path string) (*CredsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read creds file %s: %w", path, err)
	}
	return ParseCredsData(data, path)
}

// ParseCredsData parses .creds content from raw bytes.
func ParseCredsData(data []byte, name string) (*CredsFile, error) {
	token, err := jwt.ParseDecoratedJWT(data)
	if err != nil {
		return nil, fmt.Errorf("parse JWT from creds %s: %w", name, err)
	}

	kp, err := nkeys.ParseDecoratedNKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse nkey from creds %s: %w", name, err)
	}
	seed, err := kp.Seed()
	if err != nil {
		return nil, fmt.Errorf("extract seed from creds %s: %w", name, err)
	}

	return &CredsFile{
		JWT:  token,
		Seed: seed,
		Path: name,
	}, nil
}

// NATSOption returns a nats.Option that authenticates using this creds file.
// If the creds were loaded from a file path, it uses UserCredentials (which
// supports key rotation). Otherwise, it uses inline JWT + sign callback.
func (cf *CredsFile) NATSOption() nats.Option {
	if cf.Path != "" {
		return nats.UserCredentials(cf.Path)
	}
	return cf.inlineNATSOption()
}

func (cf *CredsFile) inlineNATSOption() nats.Option {
	token := cf.JWT
	seed := cf.Seed
	return nats.UserJWT(
		func() (string, error) {
			return token, nil
		},
		func(nonce []byte) ([]byte, error) {
			kp, err := nkeys.FromSeed(seed)
			if err != nil {
				return nil, err
			}
			return kp.Sign(nonce)
		},
	)
}

// NATSOptionFromSeed returns a nats.Option that authenticates using raw
// nkey challenge-response (no JWT). This is suitable for server-level
// authentication where the server is configured with a list of trusted
// public keys.
func NATSOptionFromSeed(seed []byte) (nats.Option, error) {
	pub, err := PublicKeyFromSeed(seed)
	if err != nil {
		return nil, fmt.Errorf("extract public key from seed: %w", err)
	}
	seedCopy := make([]byte, len(seed))
	copy(seedCopy, seed)
	opt := nats.Nkey(pub, func(nonce []byte) ([]byte, error) {
		kp, err := nkeys.FromSeed(seedCopy)
		if err != nil {
			return nil, err
		}
		return kp.Sign(nonce)
	})
	return opt, nil
}

// NATSOptionsForPeel returns a set of NATS options appropriate for a peel
// connection, including authentication, connection name, and reconnect
// behavior.
func NATSOptionsForPeel(credsPath string, peelID string) []nats.Option {
	return []nats.Option{
		nats.UserCredentials(credsPath),
		nats.Name(fmt.Sprintf("zester-peel-%s", peelID)),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(nats.DefaultReconnectWait),
		nats.ReconnectBufSize(8 * 1024 * 1024),
	}
}

// NATSOptionsForMaster returns NATS options for a master connection.
func NATSOptionsForMaster(credsPath string) []nats.Option {
	return []nats.Option{
		nats.UserCredentials(credsPath),
		nats.Name("zester-master"),
		nats.MaxReconnects(-1),
	}
}

// BootstrapPeelCreds generates a complete set of peel credentials:
// creates a user key pair, signs a user JWT with the account key,
// and writes a .creds file.
func BootstrapPeelCreds(
	accountKP *KeyBundle,
	peelID string,
	credsPath string,
) (*KeyBundle, string, error) {
	userKP, err := GenerateKeyBundle(RoleUser)
	if err != nil {
		return nil, "", fmt.Errorf("generate peel user key: %w", err)
	}

	opts := PeelUserJWTOptions(peelID, accountKP.PublicKey)
	userJWT, err := CreateUserJWT(userKP, accountKP, opts)
	if err != nil {
		return nil, "", fmt.Errorf("create peel user JWT: %w", err)
	}

	if err := WriteCredsFile(credsPath, userJWT, userKP.Seed); err != nil {
		return nil, "", fmt.Errorf("write peel creds: %w", err)
	}

	return userKP, userJWT, nil
}
