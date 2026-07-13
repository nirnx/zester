package modschema_test

import (
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
)

func TestValidateEffects_StateRequiresCheckAndApply(t *testing.T) {
	cases := []struct {
		name    string
		effects modschema.Effects
		wantErr bool
	}{
		{"both present", modschema.Effects{Check: "c", Apply: "a"}, false},
		{"revert optional", modschema.Effects{Check: "c", Apply: "a", Revert: "r"}, false},
		{"missing check", modschema.Effects{Apply: "a"}, true},
		{"missing apply", modschema.Effects{Check: "c"}, true},
		{"missing both", modschema.Effects{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := modschema.ValidateEffects(modschema.KindState, tc.effects)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateEffects(state, %+v) error = %v, wantErr %v", tc.effects, err, tc.wantErr)
			}
		})
	}
}

func TestValidateEffects_ExecRequiresExecutionOnly(t *testing.T) {
	if err := modschema.ValidateEffects(modschema.KindExec, modschema.Effects{Execution: "e"}); err != nil {
		t.Errorf("ValidateEffects(exec, Execution only) = %v, want nil", err)
	}
	if err := modschema.ValidateEffects(modschema.KindExec, modschema.Effects{}); err == nil {
		t.Error("ValidateEffects(exec, empty) = nil, want error for missing Execution")
	}
}

func TestValidateEffects_DispatchRequiresExecutionOnly(t *testing.T) {
	if err := modschema.ValidateEffects(modschema.KindDispatch, modschema.Effects{Execution: "e"}); err != nil {
		t.Errorf("ValidateEffects(dispatch, Execution only) = %v, want nil", err)
	}
	if err := modschema.ValidateEffects(modschema.KindDispatch, modschema.Effects{}); err == nil {
		t.Error("ValidateEffects(dispatch, empty) = nil, want error for missing Execution")
	}
}

func TestValidateEffects_NoFakeCheckApplyForPhaseLessSurfaces(t *testing.T) {
	for _, kind := range []modschema.Kind{modschema.KindExec, modschema.KindDispatch} {
		for _, e := range []modschema.Effects{
			{Execution: "e", Check: "fake"},
			{Execution: "e", Apply: "fake"},
			{Execution: "e", Revert: "fake"},
		} {
			if err := modschema.ValidateEffects(kind, e); err == nil {
				t.Errorf("ValidateEffects(%s, %+v) = nil, want error for fake phase prose", kind, e)
			}
		}
	}
}

func TestValidateEffects_UnknownKind(t *testing.T) {
	if err := modschema.ValidateEffects(modschema.Kind("bogus"), modschema.Effects{}); err == nil {
		t.Error("ValidateEffects(bogus kind) = nil, want error")
	}
}
