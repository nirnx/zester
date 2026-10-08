package auth

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/jwt/v2"
)

// Credential revocation.
//
// NATS has exactly one server-side mechanism to invalidate an issued user JWT
// before it expires: the ACCOUNT JWT carries a revocation list (user public
// key → unix time; every JWT of that user issued at or before that time is
// rejected, and live connections holding one are closed with "User
// Authentication Revoked"). Updating the list therefore means re-signing the
// account JWT with a key the operator trusts and pushing it to the running
// nats-server over the system account. The helpers below implement both
// halves; internal/masterd wires them to `zester enroll revoke`.

// AccountJWTWithRevocations decodes baseAccountJWT, REPLACES its revocation
// list with revoked (user public key → revocation time), and re-encodes it
// signed by signingKP — an operator identity or signing key listed in the
// operator JWT. All other claims (name, limits, JetStream, signing keys) are
// carried over unchanged. An empty revoked map yields an account JWT with no
// revocations, which is how a fully re-enrolled fleet converges back.
func AccountJWTWithRevocations(baseAccountJWT string, signingKP *KeyBundle, revoked map[string]time.Time) (string, error) {
	if signingKP == nil || signingKP.Role != RoleOperator {
		return "", fmt.Errorf("auth: account revocation requires an operator (signing) key")
	}
	ac, err := jwt.DecodeAccountClaims(baseAccountJWT)
	if err != nil {
		return "", fmt.Errorf("auth: decode base account JWT: %w", err)
	}
	ac.Revocations = jwt.RevocationList{}
	for pub, at := range revoked {
		if err := ValidatePublicKey(pub, RoleUser); err != nil {
			return "", fmt.Errorf("auth: revoke %q: %w", pub, err)
		}
		if at.IsZero() {
			at = time.Now()
		}
		ac.Revocations.Revoke(pub, at)
	}
	token, err := ac.Encode(signingKP.KeyPair)
	if err != nil {
		return "", fmt.Errorf("auth: encode account JWT: %w", err)
	}
	return token, nil
}

// AccountClaimsUpdateSubject is the system-account request subject the
// nats-server answers with the result of applying a pushed account JWT.
func AccountClaimsUpdateSubject(accountPub string) string {
	return fmt.Sprintf("$SYS.REQ.ACCOUNT.%s.CLAIMS.UPDATE", accountPub)
}

// claimsUpdateResponse mirrors the nats-server ServerAPIResponse envelope for
// an account claims update: either Data (code 200 + a message) or Error.
type claimsUpdateResponse struct {
	Server *struct {
		Name string `json:"name"`
	} `json:"server"`
	Data *struct {
		Account string `json:"account"`
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"data"`
	Error *struct {
		Code        int    `json:"code"`
		Description string `json:"description"`
	} `json:"error"`
}

// ClaimsUpdateOutcome classifies one server's reply to an account claims
// update. Every server in a cluster answers the request: a server that has
// the account loaded (at least one client of that account connected since it
// started) applies the JWT and reports Applied; a server without the account
// reports Skipped — it will fetch the account from its resolver on the next
// connect, which for the MEMORY resolver is the preloaded (pre-revocation)
// JWT, so the master re-pushes on account connect events and periodically.
type ClaimsUpdateOutcome int

const (
	ClaimsUpdateApplied ClaimsUpdateOutcome = iota
	ClaimsUpdateSkipped
)

// ClaimsUpdateReply is one parsed server reply.
type ClaimsUpdateReply struct {
	Server  string
	Outcome ClaimsUpdateOutcome
}

// ParseClaimsUpdateReply interprets a single nats-server reply to an account
// claims update request. An error means that server rejected the JWT (or the
// reply was not a nats-server envelope at all).
func ParseClaimsUpdateReply(data []byte) (ClaimsUpdateReply, error) {
	var resp claimsUpdateResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return ClaimsUpdateReply{}, fmt.Errorf("auth: decode claims update reply: %w (%q)", err, truncate(string(data), 200))
	}
	r := ClaimsUpdateReply{}
	if resp.Server != nil {
		r.Server = resp.Server.Name
	}
	if resp.Error != nil {
		return r, fmt.Errorf("auth: nats-server %s rejected account JWT update: %d %s", r.Server, resp.Error.Code, resp.Error.Description)
	}
	if resp.Data == nil {
		return r, fmt.Errorf("auth: claims update reply carried neither data nor error (%q)", truncate(string(data), 200))
	}
	msg := strings.ToLower(resp.Data.Message)
	switch {
	case resp.Data.Code == 200 && strings.Contains(msg, "jwt updated"):
		r.Outcome = ClaimsUpdateApplied
	case resp.Data.Code == 200 && strings.Contains(msg, "skipped"):
		r.Outcome = ClaimsUpdateSkipped
	default:
		return r, fmt.Errorf("auth: nats-server %s account JWT update not applied: %d %s", r.Server, resp.Data.Code, resp.Data.Message)
	}
	return r, nil
}

// ParseClaimsUpdateResponse is the single-reply convenience form: nil when
// the server applied or skipped the update, an error otherwise.
func ParseClaimsUpdateResponse(data []byte) error {
	_, err := ParseClaimsUpdateReply(data)
	return err
}

// AccountConnectEventSubject is the system-account subject the nats-server
// publishes a client-connect event on for every connection into accountPub —
// including a revoked peel's reconnect attempt to a server that still holds
// the pre-revocation account JWT. The master uses it as a re-push trigger.
func AccountConnectEventSubject(accountPub string) string {
	return fmt.Sprintf("$SYS.ACCOUNT.%s.CONNECT", accountPub)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
