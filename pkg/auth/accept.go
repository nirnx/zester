package auth

import (
	"fmt"
	"sync"
	"time"
)

// AcceptPolicy determines how new peel keys are accepted by the master.
type AcceptPolicy int

const (
	// AcceptManual requires an administrator to explicitly accept each peel key.
	AcceptManual AcceptPolicy = iota

	// AcceptAutoTrusted auto-accepts peels whose user JWT is signed by a
	// trusted operator/account chain. This is the default for production.
	AcceptAutoTrusted

	// AcceptAutoAll auto-accepts all peels. Only for development/testing.
	AcceptAutoAll
)

func (p AcceptPolicy) String() string {
	switch p {
	case AcceptManual:
		return "manual"
	case AcceptAutoTrusted:
		return "auto-trusted"
	case AcceptAutoAll:
		return "auto-all"
	default:
		return "unknown"
	}
}

// ParseAcceptPolicy converts a string to an AcceptPolicy.
func ParseAcceptPolicy(s string) (AcceptPolicy, error) {
	switch s {
	case "manual":
		return AcceptManual, nil
	case "auto-trusted", "auto_trusted":
		return AcceptAutoTrusted, nil
	case "auto-all", "auto_all":
		return AcceptAutoAll, nil
	default:
		return AcceptManual, fmt.Errorf("unknown accept policy: %q", s)
	}
}

// KeyState represents the lifecycle state of a peel's key.
type KeyState int

const (
	KeyPending  KeyState = iota // awaiting acceptance
	KeyAccepted                 // accepted and authorized
	KeyRejected                 // explicitly rejected
	KeyRevoked                  // previously accepted, now revoked
)

func (s KeyState) String() string {
	switch s {
	case KeyPending:
		return "pending"
	case KeyAccepted:
		return "accepted"
	case KeyRejected:
		return "rejected"
	case KeyRevoked:
		return "revoked"
	default:
		return "unknown"
	}
}

// KeyRecord tracks the acceptance state of a peel's public key.
type KeyRecord struct {
	PeelID      string
	PublicKey   string
	CurvePubKey string
	State       KeyState
	SubmittedAt time.Time
	DecidedAt   time.Time
	DecidedBy   string
}

// KeyStore manages the acceptance state of peel keys. Implementations
// should persist records (e.g., to NATS KV). This in-memory version
// is suitable for testing and single-master setups.
type KeyStore struct {
	mu      sync.RWMutex
	policy  AcceptPolicy
	records map[string]*KeyRecord // keyed by peel ID

	// trustedAccountPubs is the set of account public keys whose signed
	// user JWTs are automatically trusted under AcceptAutoTrusted policy.
	trustedAccountPubs map[string]struct{}
}

// NewKeyStore creates a key store with the given acceptance policy.
func NewKeyStore(policy AcceptPolicy) *KeyStore {
	return &KeyStore{
		policy:             policy,
		records:            make(map[string]*KeyRecord),
		trustedAccountPubs: make(map[string]struct{}),
	}
}

// AddTrustedAccount registers an account public key as trusted for
// auto-accept.
func (ks *KeyStore) AddTrustedAccount(accountPub string) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.trustedAccountPubs[accountPub] = struct{}{}
}

// RemoveTrustedAccount removes an account from the trusted set.
func (ks *KeyStore) RemoveTrustedAccount(accountPub string) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	delete(ks.trustedAccountPubs, accountPub)
}

// SubmitKey registers a new peel key for acceptance. The return value
// indicates whether the key was automatically accepted.
func (ks *KeyStore) SubmitKey(peelID, publicKey, curvePubKey string, userJWT string) (KeyState, error) {
	ks.mu.Lock()
	defer ks.mu.Unlock()

	if existing, ok := ks.records[peelID]; ok {
		if existing.State == KeyAccepted && existing.PublicKey == publicKey {
			return KeyAccepted, nil
		}
		if existing.State == KeyRevoked {
			return KeyRevoked, fmt.Errorf("peel %s key is revoked", peelID)
		}
	}

	record := &KeyRecord{
		PeelID:      peelID,
		PublicKey:   publicKey,
		CurvePubKey: curvePubKey,
		State:       KeyPending,
		SubmittedAt: time.Now(),
	}

	switch ks.policy {
	case AcceptAutoAll:
		record.State = KeyAccepted
		record.DecidedAt = time.Now()
		record.DecidedBy = "auto-all"

	case AcceptAutoTrusted:
		if userJWT != "" {
			uc, err := DecodeUserJWT(userJWT)
			if err == nil {
				issuer := uc.Issuer
				if uc.IssuerAccount != "" {
					issuer = uc.IssuerAccount
				}
				if _, trusted := ks.trustedAccountPubs[issuer]; trusted {
					record.State = KeyAccepted
					record.DecidedAt = time.Now()
					record.DecidedBy = "auto-trusted"
				}
			}
		}

	case AcceptManual:
		// stays pending
	}

	ks.records[peelID] = record
	return record.State, nil
}

// AcceptKey manually accepts a pending peel key.
func (ks *KeyStore) AcceptKey(peelID, decidedBy string) error {
	ks.mu.Lock()
	defer ks.mu.Unlock()

	record, ok := ks.records[peelID]
	if !ok {
		return fmt.Errorf("no key record for peel %s", peelID)
	}
	if record.State != KeyPending {
		return fmt.Errorf("peel %s key is %s, not pending", peelID, record.State)
	}
	record.State = KeyAccepted
	record.DecidedAt = time.Now()
	record.DecidedBy = decidedBy
	return nil
}

// RejectKey manually rejects a pending peel key.
func (ks *KeyStore) RejectKey(peelID, decidedBy string) error {
	ks.mu.Lock()
	defer ks.mu.Unlock()

	record, ok := ks.records[peelID]
	if !ok {
		return fmt.Errorf("no key record for peel %s", peelID)
	}
	if record.State != KeyPending {
		return fmt.Errorf("peel %s key is %s, not pending", peelID, record.State)
	}
	record.State = KeyRejected
	record.DecidedAt = time.Now()
	record.DecidedBy = decidedBy
	return nil
}

// RevokeKey revokes a previously accepted key.
func (ks *KeyStore) RevokeKey(peelID, decidedBy string) error {
	ks.mu.Lock()
	defer ks.mu.Unlock()

	record, ok := ks.records[peelID]
	if !ok {
		return fmt.Errorf("no key record for peel %s", peelID)
	}
	if record.State != KeyAccepted {
		return fmt.Errorf("peel %s key is %s, not accepted", peelID, record.State)
	}
	record.State = KeyRevoked
	record.DecidedAt = time.Now()
	record.DecidedBy = decidedBy
	return nil
}

// GetRecord returns the key record for a peel.
func (ks *KeyStore) GetRecord(peelID string) (*KeyRecord, bool) {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	r, ok := ks.records[peelID]
	if !ok {
		return nil, false
	}
	// Return a copy.
	copy := *r
	return &copy, true
}

// IsAccepted returns true if the peel's key is in accepted state.
func (ks *KeyStore) IsAccepted(peelID string) bool {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	r, ok := ks.records[peelID]
	return ok && r.State == KeyAccepted
}

// ListByState returns all key records matching the given state.
func (ks *KeyStore) ListByState(state KeyState) []*KeyRecord {
	ks.mu.RLock()
	defer ks.mu.RUnlock()

	var result []*KeyRecord
	for _, r := range ks.records {
		if r.State == state {
			copy := *r
			result = append(result, &copy)
		}
	}
	return result
}

// PendingCount returns the number of keys awaiting acceptance.
func (ks *KeyStore) PendingCount() int {
	ks.mu.RLock()
	defer ks.mu.RUnlock()

	count := 0
	for _, r := range ks.records {
		if r.State == KeyPending {
			count++
		}
	}
	return count
}

// DeleteKey removes a key record entirely. Use with caution.
func (ks *KeyStore) DeleteKey(peelID string) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	delete(ks.records, peelID)
}
