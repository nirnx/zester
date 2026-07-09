package enroll_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/auth"
	"github.com/nirnx/zester/pkg/enroll"
)

func TestVerifyEnrollSignature_Valid(t *testing.T) {
	// Generate a real user key pair.
	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	// Derive the curve public key.
	curveKey, err := auth.CurvePublicKeyFromSeed(kb.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	// Create a challenge.
	challenge := []byte("test-challenge-32-bytes-long!!!!!")

	// Sign the challenge with the curve key bound.
	signature, err := enroll.SignChallenge(kb.Seed, challenge, curveKey)
	if err != nil {
		t.Fatalf("SignChallenge: %v", err)
	}

	// Verify the signature.
	err = enroll.VerifyEnrollSignature(kb.PublicKey, challenge, signature, curveKey)
	if err != nil {
		t.Errorf("VerifyEnrollSignature failed: %v", err)
	}
}

func TestVerifyEnrollSignature_WrongPublicKey(t *testing.T) {
	// Generate two key pairs.
	kb1, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle kb1: %v", err)
	}
	kb2, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle kb2: %v", err)
	}

	curveKey, err := auth.CurvePublicKeyFromSeed(kb1.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	challenge := []byte("test-challenge-32-bytes-long!!!!!")

	// Sign with kb1.
	signature, err := enroll.SignChallenge(kb1.Seed, challenge, curveKey)
	if err != nil {
		t.Fatalf("SignChallenge: %v", err)
	}

	// Attempt to verify with kb2's public key (wrong key).
	err = enroll.VerifyEnrollSignature(kb2.PublicKey, challenge, signature, curveKey)
	if err == nil {
		t.Error("VerifyEnrollSignature with wrong public key should fail, got nil error")
	}
}

func TestVerifyEnrollSignature_TamperedChallenge(t *testing.T) {
	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	curveKey, err := auth.CurvePublicKeyFromSeed(kb.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	challenge := []byte("test-challenge-32-bytes-long!!!!!")

	signature, err := enroll.SignChallenge(kb.Seed, challenge, curveKey)
	if err != nil {
		t.Fatalf("SignChallenge: %v", err)
	}

	// Tamper with the challenge.
	tamperedChallenge := []byte("TAMPERED-challenge-32-bytes-long!!")

	// Verification should fail.
	err = enroll.VerifyEnrollSignature(kb.PublicKey, tamperedChallenge, signature, curveKey)
	if err == nil {
		t.Error("VerifyEnrollSignature with tampered challenge should fail, got nil error")
	}
}

func TestVerifyEnrollSignature_SwappedCurveKey(t *testing.T) {
	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	// Generate the correct curve key.
	curveKey1, err := auth.CurvePublicKeyFromSeed(kb.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed 1: %v", err)
	}

	// Generate a different curve key (attacker's).
	kb2, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle 2: %v", err)
	}
	curveKey2, err := auth.CurvePublicKeyFromSeed(kb2.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed 2: %v", err)
	}

	challenge := []byte("test-challenge-32-bytes-long!!!!!")

	// Sign with the correct curve key.
	signature, err := enroll.SignChallenge(kb.Seed, challenge, curveKey1)
	if err != nil {
		t.Fatalf("SignChallenge: %v", err)
	}

	// Attempt to verify with a swapped curve key (this is the security fix!).
	err = enroll.VerifyEnrollSignature(kb.PublicKey, challenge, signature, curveKey2)
	if err == nil {
		t.Error("VerifyEnrollSignature with swapped curve key should fail, got nil error")
	}
}

func TestSignChallenge_ProducesValidSignature(t *testing.T) {
	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	curveKey, err := auth.CurvePublicKeyFromSeed(kb.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	challenge := []byte("test-challenge-32-bytes-long!!!!!")

	signature, err := enroll.SignChallenge(kb.Seed, challenge, curveKey)
	if err != nil {
		t.Fatalf("SignChallenge: %v", err)
	}

	if len(signature) == 0 {
		t.Error("SignChallenge returned empty signature")
	}

	// Verify it round-trips.
	err = enroll.VerifyEnrollSignature(kb.PublicKey, challenge, signature, curveKey)
	if err != nil {
		t.Errorf("Round-trip verification failed: %v", err)
	}
}

func TestVerifyCredsSignature_Valid(t *testing.T) {
	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	enrollmentID := "enr-test123"

	// Sign the enrollment ID.
	sigB64, err := enroll.SignEnrollmentID(kb.Seed, enrollmentID)
	if err != nil {
		t.Fatalf("SignEnrollmentID: %v", err)
	}

	// Construct the auth header.
	authHeader := "Nkey " + kb.PublicKey + ":" + sigB64

	// Verify.
	err = enroll.VerifyCredsSignature(authHeader, enrollmentID, kb.PublicKey)
	if err != nil {
		t.Errorf("VerifyCredsSignature failed: %v", err)
	}
}

func TestVerifyCredsSignature_WrongPublicKey(t *testing.T) {
	kb1, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle kb1: %v", err)
	}
	kb2, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle kb2: %v", err)
	}

	enrollmentID := "enr-test123"

	sigB64, err := enroll.SignEnrollmentID(kb1.Seed, enrollmentID)
	if err != nil {
		t.Fatalf("SignEnrollmentID: %v", err)
	}

	// Send kb1's signature but claim kb2 is the expected key.
	authHeader := "Nkey " + kb1.PublicKey + ":" + sigB64

	err = enroll.VerifyCredsSignature(authHeader, enrollmentID, kb2.PublicKey)
	if err == nil {
		t.Error("VerifyCredsSignature with wrong expected key should fail, got nil error")
	}
}

func TestVerifyCredsSignature_InvalidSignature(t *testing.T) {
	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	enrollmentID := "enr-test123"

	// Create an invalid signature.
	invalidSig := base64.RawURLEncoding.EncodeToString([]byte("invalid-signature-data"))
	authHeader := "Nkey " + kb.PublicKey + ":" + invalidSig

	err = enroll.VerifyCredsSignature(authHeader, enrollmentID, kb.PublicKey)
	if err == nil {
		t.Error("VerifyCredsSignature with invalid signature should fail, got nil error")
	}
}

func TestVerifyCredsSignature_MalformedHeader(t *testing.T) {
	tests := []struct {
		name       string
		authHeader string
	}{
		{"missing Nkey prefix", "Bearer token"},
		{"missing colon", "Nkey UABC123"},
		{"empty", ""},
		{"only Nkey", "Nkey"},
		{"extra colons", "Nkey UABC123:sig1:sig2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := enroll.VerifyCredsSignature(tt.authHeader, "enr-test", "UABC123")
			if err == nil {
				t.Errorf("VerifyCredsSignature with malformed header %q should fail, got nil error", tt.authHeader)
			}
		})
	}
}

func TestValidatePeelID(t *testing.T) {
	// Peel IDs become NATS subject tokens in fixed positions
	// (zester.event.<peelID>.>, zester.job.*.return.<peelID>, ...): no dots,
	// no wildcards, no leading '_' (reserves the _master/_admin origins).
	tests := []struct {
		name    string
		peelID  string
		wantErr bool
	}{
		{"valid simple", "web-01", false},
		{"valid with underscores", "web_server_01", false},
		{"valid with hyphens", "web-server-01", false},
		{"valid alphanumeric", "webserver123", false},
		{"valid two chars", "w1", false},
		{"valid single char", "w", false},
		{"valid trailing hyphen", "web01-", false},
		{"valid trailing underscore", "web01_", false},
		{"valid interior underscore not leading", "web_01", false},
		{"valid uppercase", "WEB-01", false},
		{"valid max length", strings.Repeat("a", 128), false},
		{"empty", "", true},
		{"too long", strings.Repeat("a", 129), true},
		{"dotted", "web.01", true},
		{"dotted fqdn", "web01.example.com", true},
		{"leading dot", ".web01", true},
		{"trailing dot", "web01.", true},
		{"only dot", ".", true},
		{"leading underscore", "_web01", true},
		{"reserved master origin", "_master", true},
		{"reserved admin origin", "_admin", true},
		{"wildcard star", "web*", true},
		{"only star", "*", true},
		{"wildcard gt", "web>", true},
		{"only gt", ">", true},
		{"embedded star", "we*b01", true},
		{"starts with hyphen", "-web01", true},
		{"special chars", "web@01", true},
		{"spaces", "web 01", true},
		{"unicode letters", "wéb-01", true},
		{"unicode cyrillic", "узел-01", true},
		{"unicode emoji", "web-01-🔥", true},
		{"nul byte", "web\x0001", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := enroll.ValidatePeelID(tt.peelID)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePeelID(%q) error = %v, wantErr %v", tt.peelID, err, tt.wantErr)
			}
		})
	}
}

func TestSanitizePeelID(t *testing.T) {
	// '.' maps to '_' (peelIDDotSub), every other invalid byte to '-', the
	// leading run of '_'/'-' is stripped, and the result is truncated to 128.
	exact := []struct{ in, want string }{
		{"web-01", "web-01"},                                 // already valid, unchanged
		{"web01.example.com", "web01_example_com"},           // FQDN dots -> underscores
		{"devops-hetzner.oxm", "devops-hetzner_oxm"},         // the field case
		{"web01.pl", "web01_pl"},                             // distinct from...
		{"web01-pl", "web01-pl"},                             // ...this: '.'->'_' never collides with '-'
		{"_web01", "web01"},                                  // strip leading underscore
		{"__--web01", "web01"},                               // strip leading run of _/-
		{".web01", "web01"},                                  // leading dot -> '_' then stripped
		{"web@01", "web-01"},                                 // other punctuation -> hyphen
		{"web 01", "web-01"},                                 // space -> hyphen
		{"wéb", "w--b"},                                      // multibyte rune -> one '-' per byte
		{strings.Repeat("a", 200), strings.Repeat("a", 128)}, // truncated to 128
		{"", ""},       // empty stays empty
		{".", ""},      // all-punctuation -> empty
		{"...___", ""}, // all leading -> empty
	}
	for _, tc := range exact {
		if got := enroll.SanitizePeelID(tc.in); got != tc.want {
			t.Errorf("SanitizePeelID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// Property: for any input, the result is either "" or a valid peel ID.
	inputs := []string{
		"web-01", "web01.example.com", "_master", "*", ">", "узел-01", "web-01-🔥",
		"web\x0001", strings.Repeat("a", 300), "---", "1.2.3.4", "HOST.Example.COM",
		"a", "", ".", "@@@", "-x-",
	}
	for _, in := range inputs {
		got := enroll.SanitizePeelID(in)
		if got == "" {
			continue
		}
		if err := enroll.ValidatePeelID(got); err != nil {
			t.Errorf("SanitizePeelID(%q) = %q which fails ValidatePeelID: %v", in, got, err)
		}
	}
}

func TestDisplayPeelID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"devops-hetzner_oxm", "devops-hetzner.oxm"}, // the wire token decodes to the hostname
		{"web01_example_com", "web01.example.com"},
		{"web-01", "web-01"},     // no underscores: unchanged
		{"web01-pl", "web01-pl"}, // hyphens never decode
		{"", ""},                 // empty unchanged
		{"_master", "_master"},   // reserved origins never decode
		{"_admin", "_admin"},     // reserved origins never decode
		{"web..01", "web..01"},   // not a valid peel id: returned unchanged
		{"we b_01", "we b_01"},   // not a valid peel id: returned unchanged
		{"web01_", "web01."},     // trailing encoded dot round-trips
	}
	for _, c := range cases {
		if got := enroll.DisplayPeelID(c.in); got != c.want {
			t.Errorf("DisplayPeelID(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// Round-trip property: for any valid hostname (which can never contain
	// '_'), Display(Sanitize(h)) == h — the wire form is a lossless encoding.
	hostnames := []string{
		"devops-hetzner.oxm", "web01.example.com", "web01-pl", "a.b.c.d",
		"node-1.sub-domain.tld", "single",
	}
	for _, h := range hostnames {
		if got := enroll.DisplayPeelID(enroll.SanitizePeelID(h)); got != h {
			t.Errorf("Display(Sanitize(%q)) = %q, want round-trip", h, got)
		}
	}

	// And the inverse: Sanitize(Display(t)) == t for valid wire tokens.
	tokens := []string{"devops-hetzner_oxm", "web01_pl", "web-01", "a_b_c"}
	for _, tok := range tokens {
		if got := enroll.SanitizePeelID(enroll.DisplayPeelID(tok)); got != tok {
			t.Errorf("Sanitize(Display(%q)) = %q, want round-trip", tok, got)
		}
	}
}

func TestSanitizeGlobDots(t *testing.T) {
	cases := []struct{ in, want string }{
		{"web01.pl", "web01_pl"},                   // literal dots -> underscore
		{"*.oxm", "*_oxm"},                         // glob metachars untouched
		{"web??.example.com", "web??_example_com"}, // ? untouched
		{"web01_pl", "web01_pl"},                   // nothing to do -> unchanged
		{"web[0-9].pl", "web[0-9]_pl"},             // class untouched, outside dot substituted
		{"web[!-.]01", "web[!-.]01"},               // '.' as a range endpoint stays — substituting would widen the class
		{"web[a.z]01", "web[a.z]01"},               // '.' class member stays (inert: ids never contain '.')
		{`web\.01`, `web\.01`},                     // escaped dot stays escaped
		{"[unterminated.", "[unterminated."},       // unterminated class: conservatively treated as in-class
		{"a.b[c.d]e.f", "a_b[c.d]e_f"},             // mixed: outside substituted, inside preserved
		{"web[].]01", "web[].]01"},                 // POSIX leading ']' is a literal member — the '.' is still in-class
		{"web[!].]01", "web[!].]01"},               // same with negation
		{"[]a].b", "[]a]_b"},                       // class closes at the SECOND ']'; the outside dot substitutes
	}
	for _, c := range cases {
		if got := enroll.SanitizeGlobDots(c.in); got != c.want {
			t.Errorf("SanitizeGlobDots(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidatePublicKey(t *testing.T) {
	// Generate a valid user key.
	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle user: %v", err)
	}

	// Generate a non-user key (account).
	accountKB, err := auth.GenerateKeyBundle(auth.RoleAccount)
	if err != nil {
		t.Fatalf("GenerateKeyBundle account: %v", err)
	}

	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"valid user key", userKB.PublicKey, false},
		{"invalid account key", accountKB.PublicKey, true},
		{"empty", "", true},
		{"malformed", "invalid-key", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := enroll.ValidatePublicKey(tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePublicKey(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
		})
	}
}

func TestValidateCurvePublicKey(t *testing.T) {
	// Generate a valid curve key.
	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}
	validCurveKey, err := auth.CurvePublicKeyFromSeed(kb.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"valid X prefix", validCurveKey, false},
		{"invalid U prefix", kb.PublicKey, true},
		{"empty", "", true},
		{"malformed", "invalid-key", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := enroll.ValidateCurvePublicKey(tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCurvePublicKey(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
		})
	}
}

func TestSignEnrollmentID_RoundTrip(t *testing.T) {
	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	enrollmentID := "enr-roundtrip-test"

	sigB64, err := enroll.SignEnrollmentID(kb.Seed, enrollmentID)
	if err != nil {
		t.Fatalf("SignEnrollmentID: %v", err)
	}

	if sigB64 == "" {
		t.Error("SignEnrollmentID returned empty signature")
	}

	// Verify via VerifyCredsSignature.
	authHeader := "Nkey " + kb.PublicKey + ":" + sigB64
	err = enroll.VerifyCredsSignature(authHeader, enrollmentID, kb.PublicKey)
	if err != nil {
		t.Errorf("Round-trip verification failed: %v", err)
	}
}

func TestTrustBinding_SignVerify_AndStripResistance(t *testing.T) {
	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	challenge := make([]byte, 32)
	for i := range challenge {
		challenge[i] = byte(i)
	}
	spki := "sha256:" + strings.Repeat("ab", 32)

	sig, err := enroll.SignTrustBinding(kb.Seed, challenge, spki)
	if err != nil {
		t.Fatalf("SignTrustBinding: %v", err)
	}
	if err := enroll.VerifyTrustBinding(kb.PublicKey, challenge, spki, sig); err != nil {
		t.Fatalf("VerifyTrustBinding: %v", err)
	}
	// Wrong SPKI fails (an attacker can't rebind to a different CA).
	if err := enroll.VerifyTrustBinding(kb.PublicKey, challenge, "sha256:"+strings.Repeat("cd", 32), sig); err == nil {
		t.Error("verification accepted a different SPKI")
	}
	// Wrong challenge fails (anti-replay).
	other := make([]byte, 32)
	if err := enroll.VerifyTrustBinding(kb.PublicKey, other, spki, sig); err == nil {
		t.Error("verification accepted a different challenge")
	}
	// Domain separation: a primary enrollment signature never verifies as a
	// trust binding.
	primary, _ := enroll.SignChallenge(kb.Seed, challenge, "Xcurvekey")
	if err := enroll.VerifyTrustBinding(kb.PublicKey, challenge, spki, primary); err == nil {
		t.Error("a primary enrollment signature verified as a trust binding (domain separation broken)")
	}
}
