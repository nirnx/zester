package cmd

import (
	"errors"
	"os/user"
	"testing"
)

func TestCurrentOperator_NonEmpty(t *testing.T) {
	if got := currentOperator(); got == "" {
		t.Fatal("currentOperator() returned empty string")
	}
}

func TestCurrentOperator_UsesOSUser(t *testing.T) {
	orig := userCurrent
	defer func() { userCurrent = orig }()

	userCurrent = func() (*user.User, error) {
		return &user.User{Username: "alice"}, nil
	}
	t.Setenv("USER", "should-not-win")

	if got := currentOperator(); got != "alice" {
		t.Errorf("currentOperator() = %q, want %q", got, "alice")
	}
}

func TestCurrentOperator_EnvFallback(t *testing.T) {
	orig := userCurrent
	defer func() { userCurrent = orig }()

	userCurrent = func() (*user.User, error) {
		return nil, errors.New("user lookup failed")
	}
	t.Setenv("USER", "bob")

	if got := currentOperator(); got != "bob" {
		t.Errorf("currentOperator() = %q, want %q", got, "bob")
	}
}

func TestCurrentOperator_EmptyUsernameFallsBackToEnv(t *testing.T) {
	orig := userCurrent
	defer func() { userCurrent = orig }()

	userCurrent = func() (*user.User, error) {
		return &user.User{Username: ""}, nil
	}
	t.Setenv("USER", "carol")

	if got := currentOperator(); got != "carol" {
		t.Errorf("currentOperator() = %q, want %q", got, "carol")
	}
}

func TestCurrentOperator_Unknown(t *testing.T) {
	orig := userCurrent
	defer func() { userCurrent = orig }()

	userCurrent = func() (*user.User, error) {
		return nil, errors.New("user lookup failed")
	}
	t.Setenv("USER", "")

	if got := currentOperator(); got != "unknown" {
		t.Errorf("currentOperator() = %q, want %q", got, "unknown")
	}
}

func TestCurrentUsername_DelegatesToCurrentOperator(t *testing.T) {
	orig := userCurrent
	defer func() { userCurrent = orig }()

	userCurrent = func() (*user.User, error) {
		return &user.User{Username: "dave"}, nil
	}

	if got := currentUsername(); got != "dave" {
		t.Errorf("currentUsername() = %q, want %q", got, "dave")
	}
}
