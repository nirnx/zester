package enroll

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"

	"github.com/nats-io/nkeys"

	"github.com/nirnx/zester/pkg/auth"
)

// maxPeelIDLength bounds peel IDs; they are embedded in NATS subjects and
// KV keys, so 128 keeps every derived subject comfortably within limits.
const maxPeelIDLength = 128

// peel ID validation: leading alphanumeric, then alphanumerics, underscores,
// or hyphens (ASCII only). See ValidatePeelID for why the charset is strict.
var peelIDRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// VerifyEnrollSignature verifies that the provided signature is a valid
// Ed25519 signature of (challenge || curvePublicKey), using the given
// public key. Including the curve key in the signed message binds it
// cryptographically to the enrollment proof, preventing an attacker from
// swapping the X25519 curve key without invalidating the signature.
func VerifyEnrollSignature(publicKey string, challenge, signature []byte, curvePublicKey string) error {
	kp, err := nkeys.FromPublicKey(publicKey)
	if err != nil {
		return fmt.Errorf("enroll: invalid public key: %w", err)
	}
	msg := enrollSignatureMessage(challenge, curvePublicKey)
	if err := kp.Verify(msg, signature); err != nil {
		return fmt.Errorf("enroll: signature verification failed: %w", err)
	}
	return nil
}

// VerifyCredsSignature verifies that the peel signed the enrollment ID
// to prove key ownership when downloading credentials.
// The Authorization header format is: Nkey <public_key>:<base64url_signature>
func VerifyCredsSignature(authHeader, enrollmentID, expectedPubKey string) error {
	if !strings.HasPrefix(authHeader, "Nkey ") {
		return fmt.Errorf("enroll: invalid authorization header format")
	}

	payload := authHeader[5:]
	parts := strings.SplitN(payload, ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("enroll: invalid authorization header format")
	}

	pubKey := parts[0]
	sigB64 := parts[1]

	if pubKey != expectedPubKey {
		return fmt.Errorf("enroll: public key mismatch")
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("enroll: decode signature: %w", err)
	}

	message := []byte(enrollmentID)

	kp, err := nkeys.FromPublicKey(pubKey)
	if err != nil {
		return fmt.Errorf("enroll: invalid public key: %w", err)
	}
	if err := kp.Verify(message, sigBytes); err != nil {
		return fmt.Errorf("enroll: credential signature verification failed: %w", err)
	}

	return nil
}

// ValidatePublicKey checks that a public key is a valid User nkey.
func ValidatePublicKey(pub string) error {
	return auth.ValidatePublicKey(pub, auth.RoleUser)
}

// ValidatePeelID checks that a peel ID is well-formed. It is enforced at the
// enrollment submit path — the only place a Record is created from a request —
// so no ID violating these rules can ever enter the fleet.
//
// Peel IDs become NATS subject tokens in FIXED positions (for example
// zester.event.<peelID>.send.<tag...>, zester.job.*.return.<peelID>, and
// $KV.facts.<peelID>), so the charset must keep subject parsing unambiguous
// and forgery-resistant:
//
//   - no dots: '.' is the NATS token separator — a dotted ID would split into
//     several tokens and shift every fixed-position subject parse;
//   - no '*' or '>': NATS wildcards must never appear inside a literal token
//     (a wildcard ID could match or mask other peels' subjects);
//   - no leading '_': underscore-prefixed origin tokens (_master, _admin, and
//     any future _x source) are reserved for trusted master/operator
//     publishers that peels must never be able to impersonate.
//
// A peel ID must be non-empty, at most 128 characters, and match
// ^[a-zA-Z0-9][a-zA-Z0-9_-]*$.
func ValidatePeelID(peelID string) error {
	if peelID == "" {
		return fmt.Errorf("enroll: peel ID is required")
	}
	if len(peelID) > maxPeelIDLength {
		return fmt.Errorf("enroll: peel ID exceeds maximum length of %d characters", maxPeelIDLength)
	}
	if strings.HasPrefix(peelID, "_") {
		return fmt.Errorf("enroll: peel ID must not start with '_' (reserved for trusted event origins such as _master and _admin)")
	}
	if strings.ContainsAny(peelID, ".*>") {
		return fmt.Errorf("enroll: peel ID must not contain '.', '*', or '>' (peel IDs become NATS subject tokens)")
	}
	if !peelIDRegex.MatchString(peelID) {
		return fmt.Errorf("enroll: peel ID contains invalid characters (must match ^[a-zA-Z0-9][a-zA-Z0-9_-]*$)")
	}
	return nil
}

// ValidateCurvePublicKey checks that a curve public key has the X prefix
// and is well-formed.
func ValidateCurvePublicKey(curvePub string) error {
	if curvePub == "" {
		return fmt.Errorf("enroll: curve public key is required")
	}
	if !strings.HasPrefix(curvePub, "X") {
		return fmt.Errorf("enroll: curve public key must start with X prefix")
	}
	return nil
}

// SignChallenge signs (challenge || curvePublicKey) with the given nkey seed.
// The curve key is included in the signed message to cryptographically bind
// it to the enrollment proof, preventing curve key substitution attacks.
// Used by the peel-side enrollment client.
func SignChallenge(seed, challenge []byte, curvePublicKey string) ([]byte, error) {
	kp, err := nkeys.FromSeed(seed)
	if err != nil {
		return nil, fmt.Errorf("enroll: load key from seed: %w", err)
	}
	msg := enrollSignatureMessage(challenge, curvePublicKey)
	sig, err := kp.Sign(msg)
	if err != nil {
		return nil, fmt.Errorf("enroll: sign challenge: %w", err)
	}
	return sig, nil
}

// enrollSignatureMessage constructs the message that is signed/verified
// during enrollment: challenge_bytes || curve_public_key_bytes.
// The challenge is always 32 bytes, so the boundary is unambiguous.
func enrollSignatureMessage(challenge []byte, curvePublicKey string) []byte {
	curveBytes := []byte(curvePublicKey)
	msg := make([]byte, len(challenge)+len(curveBytes))
	copy(msg, challenge)
	copy(msg[len(challenge):], curveBytes)
	return msg
}

// SignEnrollmentID signs the enrollment ID for credential download auth.
// Returns the base64url-encoded signature.
func SignEnrollmentID(seed []byte, enrollmentID string) (string, error) {
	kp, err := nkeys.FromSeed(seed)
	if err != nil {
		return "", fmt.Errorf("enroll: load key from seed: %w", err)
	}
	sig, err := kp.Sign([]byte(enrollmentID))
	if err != nil {
		return "", fmt.Errorf("enroll: sign enrollment ID: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(sig), nil
}
