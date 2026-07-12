package cliargs

import "testing"

// These tests were relocated verbatim from cmd/zester/cmd/exec_test.go alongside
// the ParseKeyValues function; they pin the exact parsing contract the module
// framework's CLI ingress leg depends on.

func TestParseKeyValues(t *testing.T) {
	args := make(map[string]any)
	ParseKeyValues([]string{"env=prod", "region=us-east-1", "count=3"}, args)

	if args["env"] != "prod" {
		t.Errorf("args[env] = %v, want %q", args["env"], "prod")
	}
	if args["region"] != "us-east-1" {
		t.Errorf("args[region] = %v, want %q", args["region"], "us-east-1")
	}
	if args["count"] != "3" {
		t.Errorf("args[count] = %v, want %q", args["count"], "3")
	}
}

func TestParseKeyValues_NoEquals(t *testing.T) {
	args := make(map[string]any)
	ParseKeyValues([]string{"notapair", "key=value"}, args)

	if _, ok := args["notapair"]; ok {
		t.Error("non-key=value string should not be added to args")
	}
	if args["key"] != "value" {
		t.Errorf("args[key] = %v, want %q", args["key"], "value")
	}
}

func TestParseKeyValues_EmptyValue(t *testing.T) {
	args := make(map[string]any)
	ParseKeyValues([]string{"flag="}, args)

	if args["flag"] != "" {
		t.Errorf("args[flag] = %v, want empty string", args["flag"])
	}
}

func TestParseKeyValues_ValueWithEquals(t *testing.T) {
	args := make(map[string]any)
	ParseKeyValues([]string{"conn=host=db port=5432"}, args)

	// strings.Cut splits on first "=", so value is "host=db port=5432"
	if args["conn"] != "host=db port=5432" {
		t.Errorf("args[conn] = %v, want %q", args["conn"], "host=db port=5432")
	}
}

func TestParseKeyValues_Empty(t *testing.T) {
	args := make(map[string]any)
	ParseKeyValues(nil, args)
	if len(args) != 0 {
		t.Errorf("expected empty args, got %v", args)
	}
}
