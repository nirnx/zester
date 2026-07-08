//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	tcexec "github.com/testcontainers/testcontainers-go/exec"
)

// execAllowFail runs a command in a container and returns (exitCode, output)
// WITHOUT failing the test on a non-zero exit — for commands that
// intentionally exit non-zero (e.g. `ca init` refusing to overwrite).
func execAllowFail(t *testing.T, service string, cmd []string) (int, string) {
	t.Helper()
	ctx := context.Background()
	container, err := stack.ServiceContainer(ctx, service)
	if err != nil {
		t.Fatalf("get container %s: %v", service, err)
	}
	code, reader, err := container.Exec(ctx, cmd, tcexec.Multiplexed())
	if err != nil {
		t.Fatalf("exec in %s %v: %v", service, cmd, err)
	}
	out, _ := io.ReadAll(reader)
	return code, string(out)
}

// ---------------------------------------------------------------------------
// Embedded CA + zero-config discovery
//
// The integration stack's playground-init generates ONE embedded CA under
// /data/auth/ca and writes ca.mode auto + nats_advertise_urls, so the master
// runs in embedded mode: it self-issues its enrollment certificate from the
// CA and serves the discovery bootstrap document. These tests exercise that
// path end to end. (The peels still pass an explicit --nats-url, which
// overrides discovery, so their connectivity is unaffected.)
// ---------------------------------------------------------------------------

// TestDiscovery_BootstrapDoc verifies GET /api/v1/enroll/ca serves the CA
// bundle and the validated advertise list, and never leaks a loopback URL.
func TestDiscovery_BootstrapDoc(t *testing.T) {
	// curl the unauthenticated bootstrap route from the admin container,
	// trusting the fleet CA (SSL_CERT_FILE is set on admin).
	out := execInContainer(t, "admin", []string{
		"curl", "-sS", "--cacert", "/data/auth/nats-ca.crt",
		"https://master:8443/api/v1/enroll/ca",
	})

	start := strings.Index(out, "{")
	end := strings.LastIndex(out, "}")
	if start < 0 || end < start {
		t.Fatalf("no JSON in bootstrap response: %s", out)
	}
	var doc struct {
		V           int      `json:"v"`
		CABundlePEM string   `json:"ca_bundle_pem"`
		Fingerprint string   `json:"fingerprint"`
		NATSURLs    []string `json:"nats_urls"`
	}
	if err := json.Unmarshal([]byte(out[start:end+1]), &doc); err != nil {
		t.Fatalf("decode bootstrap doc: %v\nraw: %s", err, out)
	}

	if !strings.Contains(doc.CABundlePEM, "BEGIN CERTIFICATE") {
		t.Errorf("bootstrap doc has no CA bundle: %q", doc.CABundlePEM)
	}
	if !strings.HasPrefix(doc.Fingerprint, "sha256:") {
		t.Errorf("bootstrap doc fingerprint = %q, want sha256: pin", doc.Fingerprint)
	}
	if len(doc.NATSURLs) == 0 {
		t.Fatal("bootstrap doc advertised no NATS URLs")
	}
	for _, u := range doc.NATSURLs {
		if !strings.HasPrefix(u, "tls://") {
			t.Errorf("advertised URL %q is not tls://", u)
		}
		if strings.Contains(u, "localhost") || strings.Contains(u, "127.0.0.1") {
			t.Errorf("advertised URL %q leaks a loopback address", u)
		}
	}
}

// TestEnroll_TrustColumnOK verifies that enrolled peels (which report the CA
// SPKI they trusted) are flagged TRUST=ok by the master — the fingerprint
// binding round-trips and matches the embedded CA root.
func TestEnroll_TrustColumnOK(t *testing.T) {
	// Poll: the enrollments KV read can return transiently empty under
	// full-suite load; wait until records are visible.
	// --state all: the default list filter is "pending", but by the time this
	// runs the peels are approved (active). Poll in case the KV read is
	// transiently empty under load.
	var out string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out = execInContainer(t, "admin", []string{"zester", "enroll", "list", "--state", "all"})
		if strings.Contains(out, "TRUST") && strings.Contains(out, "enr-") {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if !strings.Contains(out, "TRUST") {
		t.Fatalf("enroll list has no TRUST column:\n%s", out)
	}
	if strings.Contains(out, "MISMATCH!") {
		t.Errorf("an enrolled peel is trust-mismatched (unexpected in the trusted stack):\n%s", out)
	}
	// At least one enrolled peel should report ok: it reported the CA it
	// trusted (the enroll_ca root), the embedded master compared it to its
	// own root and recorded TrustChecked.
	if !strings.Contains(out, "ok") {
		t.Errorf("no enrolled peel shows TRUST=ok (binding not round-tripping?):\n%s", out)
	}
}

// TestCACLI_Offline exercises the offline `zester ca` verbs inside a container:
// init a fresh CA, read its fingerprint, and issue a NATS server cert.
func TestCACLI_Offline(t *testing.T) {
	dir := "/tmp/ca-cli-test"
	execInContainer(t, "admin", []string{"rm", "-rf", dir})
	t.Cleanup(func() { execInContainer(t, "admin", []string{"rm", "-rf", dir}) })

	initOut := execInContainer(t, "admin", []string{"zester", "ca", "init", "--dir", dir})
	if !strings.Contains(initOut, "SPKI pin:") {
		t.Fatalf("ca init did not print an SPKI pin:\n%s", initOut)
	}

	fpr := execInContainer(t, "admin", []string{"zester", "ca", "fingerprint", "--dir", dir})
	if !strings.HasPrefix(strings.TrimSpace(fpr), "sha256:") {
		t.Errorf("ca fingerprint = %q, want sha256: pin", fpr)
	}

	// init must refuse to overwrite an existing CA (exits non-zero).
	code, reinit := execAllowFail(t, "admin", []string{"zester", "ca", "init", "--dir", dir})
	if code == 0 || !strings.Contains(strings.ToLower(reinit), "already exists") {
		t.Errorf("second ca init did not refuse (exit %d):\n%s", code, reinit)
	}

	issue := execInContainer(t, "admin", []string{
		"zester", "ca", "issue", "nats-server", "--dir", dir, "--dns", "nats.example,localhost", "--out", dir + "/out",
	})
	if !strings.Contains(issue, "Issued nats-server") {
		t.Errorf("ca issue nats-server failed:\n%s", issue)
	}
}
