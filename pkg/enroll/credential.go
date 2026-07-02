package enroll

import (
	"encoding/base64"
	"fmt"
	"time"

	"github.com/ptorbus/zester/pkg/auth"
)

const (
	// DefaultJWTExpiry is the default validity period for enrolled peel JWTs.
	DefaultJWTExpiry = 180 * 24 * time.Hour // 6 months
)

// CredentialIssuer generates NATS credentials for approved peels.
// It wraps the auth package to create user JWTs signed by the account
// key without requiring the peel's private seed.
type CredentialIssuer struct {
	accountKP *auth.KeyBundle
	jwtExpiry time.Duration
}

// CredentialIssuerConfig configures credential issuance.
type CredentialIssuerConfig struct {
	// AccountKP is the account key bundle used to sign user JWTs.
	AccountKP *auth.KeyBundle

	// JWTExpiry is the validity period for issued JWTs.
	// Defaults to DefaultJWTExpiry (6 months).
	JWTExpiry time.Duration
}

// NewCredentialIssuer creates a credential issuer.
func NewCredentialIssuer(cfg CredentialIssuerConfig) (*CredentialIssuer, error) {
	if cfg.AccountKP == nil {
		return nil, fmt.Errorf("enroll: account key bundle is required")
	}
	if cfg.AccountKP.Role != auth.RoleAccount {
		return nil, fmt.Errorf("enroll: expected account key bundle, got %s", cfg.AccountKP.Role)
	}
	if cfg.JWTExpiry == 0 {
		cfg.JWTExpiry = DefaultJWTExpiry
	}

	return &CredentialIssuer{
		accountKP: cfg.AccountKP,
		jwtExpiry: cfg.JWTExpiry,
	}, nil
}

// IssuedCredentials holds the generated JWT and metadata for a peel.
type IssuedCredentials struct {
	// JWT is the signed user JWT token string.
	JWT string

	// ExpiresAt is when the JWT expires.
	ExpiresAt time.Time
}

// Issue generates a scoped user JWT for an approved peel.
// The JWT is signed by the account key with permissions scoped to
// the peel ID (using auth.PeelUserJWTOptions).
func (ci *CredentialIssuer) Issue(peelID, publicKey string) (*IssuedCredentials, error) {
	opts := auth.PeelUserJWTOptions(peelID, ci.accountKP.PublicKey)
	opts.Expiry = ci.jwtExpiry

	token, err := auth.CreateUserJWTForPublicKey(publicKey, ci.accountKP, opts)
	if err != nil {
		return nil, fmt.Errorf("enroll: create user JWT for %s: %w", peelID, err)
	}

	expiresAt := time.Now().Add(ci.jwtExpiry)

	return &IssuedCredentials{
		JWT:       token,
		ExpiresAt: expiresAt,
	}, nil
}

// EncodeJWTForTransport base64-encodes the JWT for safe HTTP transport.
func EncodeJWTForTransport(jwt string) string {
	return base64.StdEncoding.EncodeToString([]byte(jwt))
}

// DecodeJWTFromTransport decodes a base64-encoded JWT.
func DecodeJWTFromTransport(encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("enroll: decode JWT: %w", err)
	}
	return string(data), nil
}
