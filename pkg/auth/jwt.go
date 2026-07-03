package auth

import (
	"fmt"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

// OperatorJWTOptions configures operator JWT generation.
type OperatorJWTOptions struct {
	Name           string
	SigningKeys    []string // additional public keys that can sign account JWTs
	SystemAccount  string   // public key of the system account (optional)
	StrictSignKeys bool
}

// AccountJWTOptions configures account JWT generation.
type AccountJWTOptions struct {
	Name          string
	SigningKeys   []string // additional public keys for signing user JWTs
	MaxConns      int64
	MaxData       int64
	MaxPayload    int64
	AllowPub      []string
	AllowSub      []string
	IssuerAccount string // set when signed by an operator signing key
	JetStream     bool   // enable JetStream for this account (-1 unlimited)
}

// UserJWTOptions configures user JWT generation.
type UserJWTOptions struct {
	Name          string
	AllowPub      []string
	AllowSub      []string
	DenyPub       []string
	DenySub       []string
	MaxPayload    int64
	BearerToken   bool
	Expiry        time.Duration
	IssuerAccount string // the account public key (required when signed by signing key)
}

// CreateOperatorJWT generates an operator JWT signed by the operator's own key pair.
func CreateOperatorJWT(operatorKP *KeyBundle, opts OperatorJWTOptions) (string, error) {
	if operatorKP.Role != RoleOperator {
		return "", fmt.Errorf("expected operator key bundle, got %s", operatorKP.Role)
	}

	oc := jwt.NewOperatorClaims(operatorKP.PublicKey)
	oc.Name = opts.Name
	oc.StrictSigningKeyUsage = opts.StrictSignKeys

	for _, sk := range opts.SigningKeys {
		oc.SigningKeys.Add(sk)
	}
	if opts.SystemAccount != "" {
		oc.SystemAccount = opts.SystemAccount
	}

	token, err := oc.Encode(operatorKP.KeyPair)
	if err != nil {
		return "", fmt.Errorf("encode operator JWT: %w", err)
	}
	return token, nil
}

// CreateAccountJWT generates an account JWT signed by an operator key pair
// (or operator signing key).
func CreateAccountJWT(accountKP *KeyBundle, signingKP *KeyBundle, opts AccountJWTOptions) (string, error) {
	if accountKP.Role != RoleAccount {
		return "", fmt.Errorf("expected account key bundle, got %s", accountKP.Role)
	}
	if signingKP.Role != RoleOperator {
		return "", fmt.Errorf("expected operator signing key, got %s", signingKP.Role)
	}

	ac := jwt.NewAccountClaims(accountKP.PublicKey)
	ac.Name = opts.Name

	for _, sk := range opts.SigningKeys {
		ac.SigningKeys.Add(sk)
	}
	if opts.MaxConns > 0 {
		ac.Limits.Conn = opts.MaxConns
	}
	if opts.MaxData > 0 {
		ac.Limits.Data = opts.MaxData
	}
	if opts.MaxPayload > 0 {
		ac.Limits.Payload = opts.MaxPayload
	}

	if len(opts.AllowPub) > 0 {
		ac.DefaultPermissions.Pub.Allow.Add(opts.AllowPub...)
	}
	if len(opts.AllowSub) > 0 {
		ac.DefaultPermissions.Sub.Allow.Add(opts.AllowSub...)
	}

	if opts.JetStream {
		ac.Limits.JetStreamLimits.MemoryStorage = -1
		ac.Limits.JetStreamLimits.DiskStorage = -1
		ac.Limits.JetStreamLimits.Streams = -1
		ac.Limits.JetStreamLimits.Consumer = -1
	}

	token, err := ac.Encode(signingKP.KeyPair)
	if err != nil {
		return "", fmt.Errorf("encode account JWT: %w", err)
	}
	return token, nil
}

// CreateUserJWT generates a user JWT signed by an account key pair
// (or account signing key).
func CreateUserJWT(userKP *KeyBundle, signingKP *KeyBundle, opts UserJWTOptions) (string, error) {
	if userKP.Role != RoleUser {
		return "", fmt.Errorf("expected user key bundle, got %s", userKP.Role)
	}
	if signingKP.Role != RoleAccount {
		return "", fmt.Errorf("expected account signing key, got %s", signingKP.Role)
	}

	uc := jwt.NewUserClaims(userKP.PublicKey)
	uc.Name = opts.Name

	if len(opts.AllowPub) > 0 {
		uc.Pub.Allow.Add(opts.AllowPub...)
	}
	if len(opts.AllowSub) > 0 {
		uc.Sub.Allow.Add(opts.AllowSub...)
	}
	if len(opts.DenyPub) > 0 {
		uc.Pub.Deny.Add(opts.DenyPub...)
	}
	if len(opts.DenySub) > 0 {
		uc.Sub.Deny.Add(opts.DenySub...)
	}
	if opts.MaxPayload > 0 {
		uc.Limits.Payload = opts.MaxPayload
	}
	uc.BearerToken = opts.BearerToken

	if opts.Expiry > 0 {
		uc.Expires = time.Now().Add(opts.Expiry).Unix()
	}
	if opts.IssuerAccount != "" {
		uc.IssuerAccount = opts.IssuerAccount
	}

	token, err := uc.Encode(signingKP.KeyPair)
	if err != nil {
		return "", fmt.Errorf("encode user JWT: %w", err)
	}
	return token, nil
}

// CreateUserJWTForPublicKey generates a user JWT for a known public key
// without requiring the user's seed. The JWT is signed by the account
// signing key. This is used during enrollment: the peel keeps its private
// seed locally and only submits the public key.
func CreateUserJWTForPublicKey(userPub string, signingKP *KeyBundle, opts UserJWTOptions) (string, error) {
	if signingKP.Role != RoleAccount {
		return "", fmt.Errorf("expected account signing key, got %s", signingKP.Role)
	}

	uc := jwt.NewUserClaims(userPub)
	uc.Name = opts.Name

	if len(opts.AllowPub) > 0 {
		uc.Pub.Allow.Add(opts.AllowPub...)
	}
	if len(opts.AllowSub) > 0 {
		uc.Sub.Allow.Add(opts.AllowSub...)
	}
	if len(opts.DenyPub) > 0 {
		uc.Pub.Deny.Add(opts.DenyPub...)
	}
	if len(opts.DenySub) > 0 {
		uc.Sub.Deny.Add(opts.DenySub...)
	}
	if opts.MaxPayload > 0 {
		uc.Limits.Payload = opts.MaxPayload
	}
	uc.BearerToken = opts.BearerToken

	if opts.Expiry > 0 {
		uc.Expires = time.Now().Add(opts.Expiry).Unix()
	}
	if opts.IssuerAccount != "" {
		uc.IssuerAccount = opts.IssuerAccount
	}

	token, err := uc.Encode(signingKP.KeyPair)
	if err != nil {
		return "", fmt.Errorf("encode user JWT: %w", err)
	}
	return token, nil
}

// DecodeOperatorJWT decodes and validates an operator JWT string.
func DecodeOperatorJWT(token string) (*jwt.OperatorClaims, error) {
	oc, err := jwt.DecodeOperatorClaims(token)
	if err != nil {
		return nil, fmt.Errorf("decode operator JWT: %w", err)
	}
	return oc, nil
}

// DecodeAccountJWT decodes and validates an account JWT string.
func DecodeAccountJWT(token string) (*jwt.AccountClaims, error) {
	ac, err := jwt.DecodeAccountClaims(token)
	if err != nil {
		return nil, fmt.Errorf("decode account JWT: %w", err)
	}
	return ac, nil
}

// DecodeUserJWT decodes and validates a user JWT string.
func DecodeUserJWT(token string) (*jwt.UserClaims, error) {
	uc, err := jwt.DecodeUserClaims(token)
	if err != nil {
		return nil, fmt.Errorf("decode user JWT: %w", err)
	}
	return uc, nil
}

// ValidateJWTChain verifies the operator -> account -> user signing chain.
// It checks that the account JWT was signed by the operator (or its signing
// keys) and the user JWT was signed by the account (or its signing keys).
func ValidateJWTChain(operatorJWT, accountJWT, userJWT string) error {
	oc, err := DecodeOperatorJWT(operatorJWT)
	if err != nil {
		return fmt.Errorf("invalid operator JWT: %w", err)
	}

	ac, err := DecodeAccountJWT(accountJWT)
	if err != nil {
		return fmt.Errorf("invalid account JWT: %w", err)
	}

	// Check account was signed by operator or one of its signing keys.
	// OperatorClaims.SigningKeys is a StringList ([]string).
	if !isValidIssuer(ac.Issuer, oc.Subject, []string(oc.SigningKeys)) {
		return fmt.Errorf("account JWT issuer %s not trusted by operator %s", ac.Issuer, oc.Subject)
	}

	uc, err := DecodeUserJWT(userJWT)
	if err != nil {
		return fmt.Errorf("invalid user JWT: %w", err)
	}

	// Check user was signed by account or one of its signing keys.
	signingKeysList := make([]string, 0, len(ac.SigningKeys))
	for k := range ac.SigningKeys {
		signingKeysList = append(signingKeysList, k)
	}
	if !isValidIssuer(uc.Issuer, ac.Subject, signingKeysList) {
		return fmt.Errorf("user JWT issuer %s not trusted by account %s", uc.Issuer, ac.Subject)
	}

	return nil
}

func isValidIssuer(issuer, subject string, signingKeys []string) bool {
	if issuer == subject {
		return true
	}
	for _, sk := range signingKeys {
		if issuer == sk {
			return true
		}
	}
	return false
}

// GenerateFullHierarchy creates a complete operator -> account -> user chain,
// returning the key bundles and signed JWT tokens. Useful for bootstrapping
// and testing.
func GenerateFullHierarchy(operatorName, accountName, userName string) (
	operatorKP, accountKP, userKP *KeyBundle,
	operatorJWT, accountJWT, userJWT string,
	err error,
) {
	operatorKP, err = GenerateKeyBundle(RoleOperator)
	if err != nil {
		return nil, nil, nil, "", "", "", fmt.Errorf("generate operator: %w", err)
	}

	accountKP, err = GenerateKeyBundle(RoleAccount)
	if err != nil {
		return nil, nil, nil, "", "", "", fmt.Errorf("generate account: %w", err)
	}

	userKP, err = GenerateKeyBundle(RoleUser)
	if err != nil {
		return nil, nil, nil, "", "", "", fmt.Errorf("generate user: %w", err)
	}

	operatorJWT, err = CreateOperatorJWT(operatorKP, OperatorJWTOptions{
		Name: operatorName,
	})
	if err != nil {
		return nil, nil, nil, "", "", "", fmt.Errorf("create operator JWT: %w", err)
	}

	accountJWT, err = CreateAccountJWT(accountKP, operatorKP, AccountJWTOptions{
		Name: accountName,
	})
	if err != nil {
		return nil, nil, nil, "", "", "", fmt.Errorf("create account JWT: %w", err)
	}

	userJWT, err = CreateUserJWT(userKP, accountKP, UserJWTOptions{
		Name: userName,
	})
	if err != nil {
		return nil, nil, nil, "", "", "", fmt.Errorf("create user JWT: %w", err)
	}

	return
}

// MasterUserJWTOptions provides defaults suitable for the master service.
// The master needs broad access to manage all zester subjects and JetStream.
func MasterUserJWTOptions(accountPub string) UserJWTOptions {
	return UserJWTOptions{
		Name:          "zester-master",
		IssuerAccount: accountPub,
		AllowPub: []string{
			"zester.>",
			"$JS.API.>",
			"$KV.>",
			"$O.>",
			"_INBOX.>",
		},
		AllowSub: []string{
			"zester.>",
			"$JS.API.>",
			"$KV.>",
			"$O.>",
			"_INBOX.>",
		},
	}
}

// AdminUserJWTOptions provides defaults suitable for the CLI admin tool.
// The admin can send commands to any peel, dispatch/cancel jobs, read/write
// JetStream KV data (including enrollment management), read job return events,
// publish operator events on the trusted _admin origin (`zester event send`),
// watch the whole event stream (`zester event watch`), and call the reactor
// control plane (`zester reactor test`). Admin creds issued before the
// event/reactor grants were added keep working degraded (no event/reactor
// CLI) until re-issued.
func AdminUserJWTOptions(accountPub string) UserJWTOptions {
	return UserJWTOptions{
		Name:          "zester-admin",
		IssuerAccount: accountPub,
		AllowPub: []string{
			"zester.cmd.>",
			"zester.dispatch",
			"zester.job.>",
			"zester.update.>",
			// Operator events (`zester event send`) publish on the trusted
			// _admin origin only — never on peel or _master origins.
			"zester.event._admin.>",
			// Reactor control plane request/reply (`zester reactor test`).
			"zester.reactor.>",
			// Enrollment admin request/reply (approve/reject/revoke) served
			// by the masters' admin service.
			"zester.admin.>",
			// Target-resolution service (request/reply against the masters'
			// in-memory facts index); falls back to KV scans without it.
			"zester.target.resolve",
			"$JS.API.>",
			"$KV.>",
			"$O.>",
			"_INBOX.>",
		},
		AllowSub: []string{
			"zester.job.>",
			"zester.update.>",
			// Event stream watching (`zester event watch`).
			"zester.event.>",
			"$JS.API.>",
			"$KV.>",
			"$O.>",
			"_INBOX.>",
		},
	}
}

// PeelUserJWTOptions provides defaults suitable for a peel connection.
// JetStream API access is scoped to the specific KV buckets a peel needs:
//   - facts: write own facts (Put via $KV subject, bucket handle via STREAM.INFO)
//   - settings-files: read, list, watch template files
//   - secrets: read and watch per-peel encrypted secrets
//   - state-files: read, list, watch state files for local caching
//   - peel-heartbeat: write own liveness beat (bucket handle via STREAM.INFO,
//     Put scoped to the peel's own key)
//
// The peel may also publish on zester.target.resolve (request/reply to the
// master-side target-resolution service used by basket() queries; replies
// ride the _INBOX.> grant). Existing fleets enrolled before these grants were
// added need re-issued credentials: until then heartbeat puts fail (logged at
// Debug, retried) and basket resolution falls back to facts-KV scans —
// degraded, not broken.
//
// Scheduler return_job results are published on the peel-scoped
// zester.job.*.schedule.<peelID> subject (captured by the job-events
// stream and persisted by the master) — peels have NO write access to the
// jobs or job-returns KV buckets, so one compromised peel cannot tamper
// with other peels' job records or returns.
func PeelUserJWTOptions(peelID string, accountPub string) UserJWTOptions {
	return UserJWTOptions{
		Name:          peelID,
		IssuerAccount: accountPub,
		AllowPub: []string{
			fmt.Sprintf("zester.event.%s.>", peelID),
			fmt.Sprintf("zester.fact.%s", peelID),
			fmt.Sprintf("zester.job.*.ack.%s", peelID),
			fmt.Sprintf("zester.job.*.return.%s", peelID),
			fmt.Sprintf("zester.job.*.schedule.%s", peelID),
			// Target-resolution service (request/reply; replies use _INBOX.>).
			"zester.target.resolve",
			// JetStream API — facts bucket (bucket handle, KV Put, read for basket target resolution).
			"$JS.API.STREAM.INFO.KV_facts",
			"$JS.API.DIRECT.GET.KV_facts.>",
			"$JS.API.STREAM.MSG.GET.KV_facts",
			"$JS.API.CONSUMER.CREATE.KV_facts",
			"$JS.API.CONSUMER.CREATE.KV_facts.>",
			"$JS.API.CONSUMER.DELETE.KV_facts.>",
			// JetStream API — settings-files bucket (get, list, watch, cleanup).
			"$JS.API.STREAM.INFO.KV_settings-files",
			"$JS.API.DIRECT.GET.KV_settings-files.>",
			"$JS.API.STREAM.MSG.GET.KV_settings-files",
			"$JS.API.CONSUMER.CREATE.KV_settings-files",
			"$JS.API.CONSUMER.CREATE.KV_settings-files.>",
			"$JS.API.CONSUMER.DELETE.KV_settings-files.>",
			// JetStream API — secrets bucket (get, watch, cleanup).
			"$JS.API.STREAM.INFO.KV_secrets",
			"$JS.API.DIRECT.GET.KV_secrets.>",
			"$JS.API.STREAM.MSG.GET.KV_secrets",
			"$JS.API.CONSUMER.CREATE.KV_secrets",
			"$JS.API.CONSUMER.CREATE.KV_secrets.>",
			"$JS.API.CONSUMER.DELETE.KV_secrets.>",
			// JetStream API — basket bucket (put own data, read all).
			"$JS.API.STREAM.INFO.KV_basket",
			"$JS.API.DIRECT.GET.KV_basket.>",
			"$JS.API.STREAM.MSG.GET.KV_basket",
			"$JS.API.CONSUMER.CREATE.KV_basket",
			"$JS.API.CONSUMER.CREATE.KV_basket.>",
			"$JS.API.CONSUMER.DELETE.KV_basket.>",
			// JetStream API — state-files bucket (get, list, watch, cleanup).
			"$JS.API.STREAM.INFO.KV_state-files",
			"$JS.API.DIRECT.GET.KV_state-files.>",
			"$JS.API.STREAM.MSG.GET.KV_state-files",
			"$JS.API.CONSUMER.CREATE.KV_state-files",
			"$JS.API.CONSUMER.CREATE.KV_state-files.>",
			"$JS.API.CONSUMER.DELETE.KV_state-files.>",
			// JetStream API — peel-heartbeat bucket (bucket handle; the Put
			// itself is the per-peel $KV grant below, mirroring facts).
			"$JS.API.STREAM.INFO.KV_peel-heartbeat",
			fmt.Sprintf("$KV.basket.%s.>", peelID),
			fmt.Sprintf("$KV.facts.%s", peelID),
			fmt.Sprintf("$KV.peel-heartbeat.%s", peelID),
			"_INBOX.>",
		},
		AllowSub: []string{
			fmt.Sprintf("zester.cmd.%s", peelID),
			fmt.Sprintf("zester.cmd.%s.>", peelID),
			"zester.job.*.cancel",
			"$KV.settings-files.>",
			"$KV.secrets._master_curve_pub",
			fmt.Sprintf("$KV.secrets.%s", peelID),
			"$KV.basket.>",
			"$KV.state-files.>",
			"_INBOX.>",
		},
	}
}

// SigningKeyPair loads only a signing key pair from a seed, suitable for use
// as an operator or account signing key. The role is inferred from the seed
// prefix.
func SigningKeyPair(seed []byte) (nkeys.KeyPair, error) {
	kp, err := nkeys.FromSeed(seed)
	if err != nil {
		return nil, fmt.Errorf("load signing key from seed: %w", err)
	}
	return kp, nil
}
