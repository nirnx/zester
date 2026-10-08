package auth

import (
	"fmt"
	"os"
	"path/filepath"
)

// File names of the NATS operator-mode trust material that
// `zester nats-auth init` (and the playground bootstrap) write into the auth
// directory. The master reloads several of them at runtime, so the names are
// part of the operational contract — keep them stable.
const (
	// FileOperatorJWT is the operator JWT the external nats-server loads
	// (`operator: <path>`); it lists the operator signing key, so account
	// JWTs signed by that key are trusted.
	FileOperatorJWT = "operator.jwt"

	// FileAccountJWT is the zester account JWT as issued at bootstrap (no
	// revocations). The master uses it as the BASE when it re-signs the
	// account with a revocation list (see AccountJWTWithRevocations).
	FileAccountJWT = "account.jwt"

	// FileAccountSeed is the zester account identity seed: it signs every
	// peel user JWT and derives the master's settings-encryption curve key.
	FileAccountSeed = "account.seed"

	// FileOperatorSigningSeed is the operator SIGNING key seed. It signs
	// account JWTs — at bootstrap and, on the master, whenever a peel's
	// credentials are revoked (the revocation list lives in the account
	// JWT). The operator IDENTITY key is never written to disk: it signs the
	// operator JWT once and is discarded, so a compromised master can
	// re-sign accounts but never mint a new operator.
	FileOperatorSigningSeed = "operator-signing.seed"

	// FileSysCreds is a user credential in the NATS system account. The
	// master uses it to push account JWT updates (revocations) to the
	// running nats-server over $SYS.REQ.ACCOUNT.<acct>.CLAIMS.UPDATE.
	FileSysCreds = "sys.creds"

	// FileMasterCreds is the master daemon's user credential.
	FileMasterCreds = "master.creds"

	// FileAdminCreds is the operator CLI's user credential.
	FileAdminCreds = "admin.creds"
)

// HierarchyOptions names the generated operator and account.
type HierarchyOptions struct {
	// OperatorName defaults to "zester-op".
	OperatorName string
	// AccountName defaults to "zester-acct".
	AccountName string
}

// Hierarchy is a freshly generated NATS operator-mode trust hierarchy: the
// operator (identity + signing key), the zester account, the system account,
// and the three user credentials the daemons and the CLI connect with.
type Hierarchy struct {
	Operator        *KeyBundle // identity key — signs the operator JWT only
	OperatorSigning *KeyBundle // signing key — signs account JWTs (incl. revocation updates)
	Account         *KeyBundle
	SysAccount      *KeyBundle

	OperatorJWT   string
	AccountJWT    string
	SysAccountJWT string

	MasterCreds []byte
	AdminCreds  []byte
	SysCreds    []byte
}

// GenerateHierarchy creates the complete trust hierarchy in memory. Account
// JWTs are signed by the operator SIGNING key (listed in the operator JWT's
// signing keys), never by the operator identity key, so the master — which
// holds only the signing key — can later re-sign the zester account JWT with a
// revocation list and the server trusts it.
func GenerateHierarchy(opts HierarchyOptions) (*Hierarchy, error) {
	if opts.OperatorName == "" {
		opts.OperatorName = "zester-op"
	}
	if opts.AccountName == "" {
		opts.AccountName = "zester-acct"
	}

	operatorKP, err := GenerateKeyBundle(RoleOperator)
	if err != nil {
		return nil, fmt.Errorf("generate operator key: %w", err)
	}
	signingKP, err := GenerateKeyBundle(RoleOperator)
	if err != nil {
		return nil, fmt.Errorf("generate operator signing key: %w", err)
	}
	accountKP, err := GenerateKeyBundle(RoleAccount)
	if err != nil {
		return nil, fmt.Errorf("generate account key: %w", err)
	}
	sysAccountKP, err := GenerateKeyBundle(RoleAccount)
	if err != nil {
		return nil, fmt.Errorf("generate system-account key: %w", err)
	}

	operatorJWT, err := CreateOperatorJWT(operatorKP, OperatorJWTOptions{
		Name:          opts.OperatorName,
		SigningKeys:   []string{signingKP.PublicKey},
		SystemAccount: sysAccountKP.PublicKey,
	})
	if err != nil {
		return nil, fmt.Errorf("create operator JWT: %w", err)
	}
	accountJWT, err := CreateAccountJWT(accountKP, signingKP, AccountJWTOptions{
		Name:      opts.AccountName,
		JetStream: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create account JWT: %w", err)
	}
	sysAccountJWT, err := CreateAccountJWT(sysAccountKP, signingKP, AccountJWTOptions{Name: "SYS"})
	if err != nil {
		return nil, fmt.Errorf("create system-account JWT: %w", err)
	}

	h := &Hierarchy{
		Operator:        operatorKP,
		OperatorSigning: signingKP,
		Account:         accountKP,
		SysAccount:      sysAccountKP,
		OperatorJWT:     operatorJWT,
		AccountJWT:      accountJWT,
		SysAccountJWT:   sysAccountJWT,
	}

	for _, u := range []struct {
		dst  *[]byte
		name string
		opts UserJWTOptions
		acct *KeyBundle
	}{
		{&h.MasterCreds, FileMasterCreds, MasterUserJWTOptions(accountKP.PublicKey), accountKP},
		{&h.AdminCreds, FileAdminCreds, AdminUserJWTOptions(accountKP.PublicKey), accountKP},
		{&h.SysCreds, FileSysCreds, SysUserJWTOptions(sysAccountKP.PublicKey), sysAccountKP},
	} {
		userKP, err := GenerateKeyBundle(RoleUser)
		if err != nil {
			return nil, fmt.Errorf("generate %s key: %w", u.name, err)
		}
		token, err := CreateUserJWT(userKP, u.acct, u.opts)
		if err != nil {
			return nil, fmt.Errorf("create %s JWT: %w", u.name, err)
		}
		creds, err := GenerateCredsFile(token, userKP.Seed)
		if err != nil {
			return nil, fmt.Errorf("format %s: %w", u.name, err)
		}
		*u.dst = creds
	}
	return h, nil
}

// SysUserJWTOptions provides the master's system-account user: it exists
// solely to push account JWT updates (credential revocations) to the
// nats-server, so it is scoped to the account-claims request/reply surface.
func SysUserJWTOptions(sysAccountPub string) UserJWTOptions {
	return UserJWTOptions{
		Name:          "zester-master-sys",
		IssuerAccount: sysAccountPub,
		AllowPub: []string{
			"$SYS.REQ.ACCOUNT.*.CLAIMS.UPDATE",
			"$SYS.REQ.CLAIMS.UPDATE",
			"_INBOX.>",
		},
		AllowSub: []string{
			// Client-connect events of the zester account: a connect into a
			// server that still holds the pre-revocation account JWT (e.g.
			// after a nats-server restart) triggers a re-push of the list.
			"$SYS.ACCOUNT.*.CONNECT",
			"_INBOX.>",
		},
	}
}

// WriteFiles writes the hierarchy's files into dir (created 0700 if
// missing): operator.jwt and account.jwt (0644), the account and operator
// signing seeds and all three .creds files (0600). The operator identity seed
// is deliberately NOT written.
func (h *Hierarchy) WriteFiles(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create auth dir: %w", err)
	}
	writes := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{FileOperatorJWT, []byte(h.OperatorJWT), 0644},
		{FileAccountJWT, []byte(h.AccountJWT), 0644},
		{FileAccountSeed, h.Account.Seed, 0600},
		{FileOperatorSigningSeed, h.OperatorSigning.Seed, 0600},
		{FileMasterCreds, h.MasterCreds, 0600},
		{FileAdminCreds, h.AdminCreds, 0600},
		{FileSysCreds, h.SysCreds, 0600},
	}
	for _, w := range writes {
		if err := os.WriteFile(filepath.Join(dir, w.name), w.data, w.mode); err != nil {
			return fmt.Errorf("write %s: %w", w.name, err)
		}
	}
	return nil
}

// NATSServerConfOptions parameterize the generated nats-server.conf.
type NATSServerConfOptions struct {
	// Port is the client listen port (default 4222).
	Port int
	// CertFile / KeyFile are the server TLS cert + key paths (as the
	// nats-server sees them).
	CertFile, KeyFile string
	// StoreDir is the JetStream store directory.
	StoreDir string
	// OperatorJWTPath is the operator.jwt path as the nats-server sees it.
	OperatorJWTPath string
}

// NATSServerConf renders a nats-server.conf for the hierarchy: operator mode,
// MEMORY resolver preloading both accounts, JetStream, TLS, and the raised
// control-line limit a peel JWT needs. Account JWT updates (revocations) are
// pushed live by the master over the system account; the MEMORY resolver does
// not persist them, so the master re-pushes on every (re)connect.
func (h *Hierarchy) NATSServerConf(opts NATSServerConfOptions) string {
	if opts.Port == 0 {
		opts.Port = 4222
	}
	return fmt.Sprintf(`# Generated by zester. Point nats-server at this file.
port: %d

# JWT-authenticated clients send their user JWT in the CONNECT line. A peel's
# least-privilege JWT (job/facts/settings/secrets/state/update grants incl.
# JetStream flow control) serializes well past NATS's 4096-byte default, so the
# server would reject the connection with "maximum control line exceeded"
# BEFORE auth — a near-silent brick. Raise the limit to comfortably fit it.
max_control_line: 16384

tls {
  cert_file: %s
  key_file: %s
}
jetstream {
  store_dir: %s
}
operator: %s
system_account: %s
# MEMORY resolver: accounts are preloaded below. The zester master pushes
# account JWT updates (peel credential revocations) live over the system
# account; they are applied in memory and re-pushed by the master after every
# nats-server restart. Switch to a "resolver: full" directory resolver if you
# want pushed updates persisted by the server itself.
resolver: MEMORY
resolver_preload: {
  %s: %s
  %s: %s
}
`, opts.Port, opts.CertFile, opts.KeyFile, opts.StoreDir, opts.OperatorJWTPath,
		h.SysAccount.PublicKey,
		h.Account.PublicKey, h.AccountJWT,
		h.SysAccount.PublicKey, h.SysAccountJWT)
}
