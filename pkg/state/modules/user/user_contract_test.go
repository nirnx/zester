package usermod

import (
	"errors"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/schematest"
)

// TestUserPresentContract replays the permanent differential contract fixtures
// against the migrated user.present decoder. The cases were approved by the
// legacy-vs-new equivalence comparison while the legacy constructor still existed
// (see the migration changelog); after its deletion this replay is the permanent
// regression guard for the decode behavior — including the flagged BD-1 (msgpack
// sized-int uid/gid honored), BD-2 (CLI string coercions honored), BD-4 (all-digit
// string gid = numeric GID), BD-5 (StringList scalar-sprint / nested-reject), BD-6
// (wrong-typed values coerced/rejected), and BD-7 (integer bool coercion)
// divergences. The decode wrapper runs resolveGroupFacets so the contract can
// project the derived GID / PrimaryGroup facets exactly as the builder computes
// them (mirroring service.dead's DisableOnApply projection).
func TestUserPresentContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var u UserPresent
		if _, err := userPresentSpec.Decode(id, config, &u, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		u.resolveGroupFacets()
		return &u, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/user.present.yaml")
}

// TestUserPresentPasswordSensitive pins the rule-5 requirement: the password
// parameter is marked sensitive, and a wrong-typed password's decode error
// redacts the offending value across the ENTIRE error chain — no substring of the
// raw value survives in the rendered message or on the Unwrap chain.
func TestUserPresentPasswordSensitive(t *testing.T) {
	// The schema flags password sensitive (and never renders a default for it).
	var passwordField *modschema.Field
	for i := range userPresentSpec.Params.Fields {
		if userPresentSpec.Params.Fields[i].Name == "password" {
			passwordField = &userPresentSpec.Params.Fields[i]
			break
		}
	}
	if passwordField == nil {
		t.Fatal("password field missing from the user.present schema")
	}
	if !passwordField.Sensitive {
		t.Error("password field must be marked sensitive")
	}
	if passwordField.JSONSchema["writeOnly"] != true {
		t.Errorf("sensitive password JSON Schema must carry writeOnly, got %v", passwordField.JSONSchema)
	}

	// A composite value into the sensitive string field is a wrong-type error;
	// its value must be redacted, and the secret substring must appear NOWHERE.
	const secret = "topSecretHash"
	var u UserPresent
	_, err := userPresentSpec.Decode("web", map[string]any{
		"name":     "deploy",
		"password": []any{secret},
	}, &u, modschema.DecodeOptions{})
	if err == nil {
		t.Fatal("expected a decode error for a composite password value")
	}
	var fe *modschema.FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("expected a *modschema.FieldError, got %T: %v", err, err)
	}
	if fe.Kind != modschema.ErrWrongType {
		t.Errorf("password error kind = %s, want wrong_type", fe.Kind)
	}
	// Walk the whole chain: no rendered text may contain the secret substring.
	for e := error(fe); e != nil; e = errors.Unwrap(e) {
		if strings.Contains(e.Error(), secret) {
			t.Fatalf("secret leaked into error chain: %q", e.Error())
		}
	}
	if strings.Contains(fe.Error(), secret) {
		t.Fatalf("secret leaked into top-level error: %q", fe.Error())
	}
}
