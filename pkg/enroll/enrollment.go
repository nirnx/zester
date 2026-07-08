// Package enroll implements the manual-approval enrollment flow for peels.
// A peel generates an nkey pair, proves ownership via challenge/response,
// and waits for an administrator to approve the enrollment before receiving
// NATS credentials (JWT + seed).
package enroll

import (
	"time"
)

// State represents a step in the enrollment lifecycle.
type State string

const (
	StatePending  State = "pending"  // awaiting admin approval
	StateApproved State = "approved" // approved, credentials not yet fetched
	StateRejected State = "rejected" // explicitly rejected by admin
	StateIssued   State = "issued"   // credentials issued and fetched by peel
	StateActive   State = "active"   // peel connected to NATS and publishing facts
	StateRevoked  State = "revoked"  // previously active, credentials revoked
)

// ValidStates is the set of all known enrollment states.
var ValidStates = []State{
	StatePending, StateApproved, StateRejected,
	StateIssued, StateActive, StateRevoked,
}

// ParseState converts a string to a State. Returns ok=false if unknown.
func ParseState(s string) (State, bool) {
	switch State(s) {
	case StatePending, StateApproved, StateRejected,
		StateIssued, StateActive, StateRevoked:
		return State(s), true
	default:
		return "", false
	}
}

// Record tracks a single peel enrollment request through its lifecycle.
// Stored in NATS KV bucket "enrollments" keyed by enrollment ID.
type Record struct {
	// ID is a unique enrollment identifier ("enr-" + KSUID).
	ID string `msgpack:"id" json:"id"`

	// PeelID is the human-readable identifier the peel wants to register as.
	PeelID string `msgpack:"peel_id" json:"peel_id"`

	// PublicKey is the peel's Ed25519 nkey public key (prefix U).
	PublicKey string `msgpack:"public_key" json:"public_key"`

	// CurvePublicKey is the peel's X25519 curve public key (prefix X)
	// for settings encryption.
	CurvePublicKey string `msgpack:"curve_public_key" json:"curve_public_key"`

	// State is the current enrollment state.
	State State `msgpack:"state" json:"state"`

	// Hostname is an optional hostname reported by the peel.
	Hostname string `msgpack:"hostname,omitempty" json:"hostname,omitempty"`

	// Metadata is peel-provided metadata (os, arch, instance ID, etc.).
	Metadata map[string]string `msgpack:"metadata,omitempty" json:"metadata,omitempty"`

	// CreatedAt is when the enrollment was first submitted.
	CreatedAt time.Time `msgpack:"created_at" json:"created_at"`

	// UpdatedAt is when the record was last modified.
	UpdatedAt time.Time `msgpack:"updated_at" json:"updated_at"`

	// DecidedBy is the admin identity that approved/rejected/revoked.
	DecidedBy string `msgpack:"decided_by,omitempty" json:"decided_by,omitempty"`

	// DecidedAt is when the approval/rejection decision was made.
	DecidedAt *time.Time `msgpack:"decided_at,omitempty" json:"decided_at,omitempty"`

	// RejectReason is an optional explanation for rejection.
	RejectReason string `msgpack:"reject_reason,omitempty" json:"reject_reason,omitempty"`

	// IssuedAt is when credentials were issued and fetched.
	IssuedAt *time.Time `msgpack:"issued_at,omitempty" json:"issued_at,omitempty"`

	// ExpiresAt is the JWT expiry time.
	ExpiresAt *time.Time `msgpack:"expires_at,omitempty" json:"expires_at,omitempty"`

	// RemoteAddr is the IP address the enrollment request came from.
	RemoteAddr string `msgpack:"remote_addr,omitempty" json:"remote_addr,omitempty"`

	// TrustedCASPKI is the CA SPKI pin the peel reported it trusted for the
	// enrollment TLS connection (bound under the peel's signature). Empty
	// for pre-feature peels.
	TrustedCASPKI string `msgpack:"trusted_ca_spki,omitempty" json:"trusted_ca_spki,omitempty"`

	// TrustMismatch is true when the peel's reported TrustedCASPKI did not
	// match the master's own CA root — a first-contact MITM artifact. A
	// mismatched record is flagged in `zester enroll list/show`, and
	// `enroll approve` refuses it without --force.
	TrustMismatch bool `msgpack:"trust_mismatch,omitempty" json:"trust_mismatch,omitempty"`

	// TrustChecked is true when the master actually compared the reported
	// TrustedCASPKI against its own CA root (embedded-CA mode). In external
	// mode there is no master root to compare against, so a reported CA is
	// recorded but unverified — the CLI shows "present (unverified)" rather
	// than a false "ok".
	TrustChecked bool `msgpack:"trust_checked,omitempty" json:"trust_checked,omitempty"`

	// Revision is the KV CAS revision for optimistic concurrency control.
	Revision uint64 `msgpack:"-" json:"-"`
}

// validTransitions defines the allowed state machine transitions.
var validTransitions = map[State][]State{
	StatePending:  {StateApproved, StateRejected},
	StateApproved: {StateIssued, StateRevoked},
	StateIssued:   {StateActive, StateRevoked},
	StateActive:   {StateRevoked},
}

// CanTransitionTo returns true if moving from the current state to the
// target state is valid according to the enrollment state machine.
func (r *Record) CanTransitionTo(target State) bool {
	allowed, ok := validTransitions[r.State]
	if !ok {
		return false
	}
	for _, s := range allowed {
		if s == target {
			return true
		}
	}
	return false
}

// EnrollmentSummary is a compact representation for list endpoints.
type EnrollmentSummary struct {
	ID        string            `json:"id"`
	PeelID    string            `json:"peel_id"`
	PublicKey string            `json:"public_key"`
	Hostname  string            `json:"hostname"`
	State     State             `json:"state"`
	CreatedAt time.Time         `json:"created_at"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// Summary converts a full Record to a summary for list display.
func (r *Record) Summary() EnrollmentSummary {
	return EnrollmentSummary{
		ID:        r.ID,
		PeelID:    r.PeelID,
		PublicKey: r.PublicKey,
		Hostname:  r.Hostname,
		State:     r.State,
		CreatedAt: r.CreatedAt,
		Metadata:  r.Metadata,
	}
}
