package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nats-io/jwt/v2"
)

func TestGenerateHierarchy_ChainAndSigningKey(t *testing.T) {
	h, err := GenerateHierarchy(HierarchyOptions{})
	if err != nil {
		t.Fatalf("GenerateHierarchy: %v", err)
	}

	oc, err := jwt.DecodeOperatorClaims(h.OperatorJWT)
	if err != nil {
		t.Fatal(err)
	}
	if oc.Name != "zester-op" || oc.SystemAccount != h.SysAccount.PublicKey {
		t.Errorf("operator claims: name=%q sys=%q", oc.Name, oc.SystemAccount)
	}
	if !oc.SigningKeys.Contains(h.OperatorSigning.PublicKey) {
		t.Errorf("operator JWT must list the signing key %s", h.OperatorSigning.PublicKey)
	}
	if h.Operator.PublicKey == h.OperatorSigning.PublicKey {
		t.Errorf("identity and signing key must differ")
	}

	ac, err := jwt.DecodeAccountClaims(h.AccountJWT)
	if err != nil {
		t.Fatal(err)
	}
	if ac.Issuer != h.OperatorSigning.PublicKey {
		t.Errorf("account JWT must be signed by the operator SIGNING key, got issuer %s", ac.Issuer)
	}
	if ac.Limits.JetStreamLimits.DiskStorage != -1 {
		t.Errorf("zester account must have JetStream enabled")
	}
	sac, err := jwt.DecodeAccountClaims(h.SysAccountJWT)
	if err != nil {
		t.Fatal(err)
	}
	if sac.Name != "SYS" || sac.Issuer != h.OperatorSigning.PublicKey {
		t.Errorf("system account claims: name=%q issuer=%s", sac.Name, sac.Issuer)
	}

	for name, creds := range map[string][]byte{"master": h.MasterCreds, "admin": h.AdminCreds, "sys": h.SysCreds} {
		cf, err := ParseCredsData(creds, name)
		if err != nil {
			t.Fatalf("%s creds: %v", name, err)
		}
		uc, err := jwt.DecodeUserClaims(cf.JWT)
		if err != nil {
			t.Fatalf("%s user JWT: %v", name, err)
		}
		wantIssuer := h.Account.PublicKey
		if name == "sys" {
			wantIssuer = h.SysAccount.PublicKey
			if !uc.Pub.Allow.Contains("$SYS.REQ.ACCOUNT.*.CLAIMS.UPDATE") {
				t.Errorf("sys user must be allowed to push account claims updates: %v", uc.Pub.Allow)
			}
			if !uc.Sub.Allow.Contains("$SYS.ACCOUNT.*.CONNECT") {
				t.Errorf("sys user must be allowed to watch account connect events: %v", uc.Sub.Allow)
			}
		}
		if uc.Issuer != wantIssuer {
			t.Errorf("%s user issuer = %s, want %s", name, uc.Issuer, wantIssuer)
		}
		pub, _ := PublicKeyFromSeed(cf.Seed)
		if pub != uc.Subject {
			t.Errorf("%s creds seed does not match the JWT subject", name)
		}
	}
}

func TestHierarchy_WriteFilesAndConf(t *testing.T) {
	h, err := GenerateHierarchy(HierarchyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "auth")
	if err := h.WriteFiles(dir); err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	for _, f := range []struct {
		name string
		mode os.FileMode
	}{
		{FileOperatorJWT, 0644}, {FileAccountJWT, 0644},
		{FileAccountSeed, 0600}, {FileOperatorSigningSeed, 0600},
		{FileMasterCreds, 0600}, {FileAdminCreds, 0600}, {FileSysCreds, 0600},
	} {
		info, err := os.Stat(filepath.Join(dir, f.name))
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		if info.Mode().Perm() != f.mode {
			t.Errorf("%s mode = %v, want %v", f.name, info.Mode().Perm(), f.mode)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "operator.seed")); !os.IsNotExist(err) {
		t.Errorf("the operator identity seed must never be written")
	}

	// The signing seed round-trips and signs an account update the operator trusts.
	signing, err := LoadKeyBundleFromFile(RoleOperator, filepath.Join(dir, FileOperatorSigningSeed))
	if err != nil {
		t.Fatal(err)
	}
	if signing.PublicKey != h.OperatorSigning.PublicKey {
		t.Errorf("signing seed round-trip mismatch")
	}

	conf := h.NATSServerConf(NATSServerConfOptions{
		CertFile: "/c.crt", KeyFile: "/k.key", StoreDir: "/js", OperatorJWTPath: "/auth/operator.jwt",
	})
	for _, want := range []string{
		"port: 4222", "max_control_line: 16384", "cert_file: /c.crt", "key_file: /k.key",
		"store_dir: /js", "operator: /auth/operator.jwt",
		"system_account: " + h.SysAccount.PublicKey, "resolver: MEMORY",
		h.Account.PublicKey + ": " + h.AccountJWT, h.SysAccount.PublicKey + ": " + h.SysAccountJWT,
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("nats-server.conf missing %q:\n%s", want, conf)
		}
	}
}
