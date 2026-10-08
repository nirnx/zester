package famshared

import (
	"strings"
	"testing"
)

func TestNoControlChars(t *testing.T) {
	ok := []string{"", "plain", "with spaces inside", "deploy@ci", "unicode ✓ é", "trailing space "}
	for _, v := range ok {
		if err := NoControlChars("f", v); err != nil {
			t.Errorf("NoControlChars(%q): unexpected error %v", v, err)
		}
	}
	bad := map[string]string{
		"newline":         "a\nb",
		"carriage return": "a\rb",
		"tab":             "a\tb",
		"NUL":             "a\x00b",
		"DEL":             "a\x7fb",
		"C1 control":      "a\u0085b",
		"leading newline": "\nx",
		"only newline":    "\n",
	}
	for name, v := range bad {
		err := NoControlChars("ip", v)
		if err == nil {
			t.Errorf("%s: NoControlChars(%q): expected error, got nil", name, v)
			continue
		}
		if !strings.HasPrefix(err.Error(), "ip: ") {
			t.Errorf("%s: error %q must be prefixed with the field name", name, err)
		}
		if !strings.Contains(err.Error(), "U+") {
			t.Errorf("%s: error %q must name the offending code point", name, err)
		}
	}
}

func TestNoWhitespace(t *testing.T) {
	ok := []string{"", "web01", "web01.example.com", "ssh-ed25519", "*/5", "defaults,noatime", "UUID=abc-123"}
	for _, v := range ok {
		if err := NoWhitespace("f", v); err != nil {
			t.Errorf("NoWhitespace(%q): unexpected error %v", v, err)
		}
	}
	bad := map[string]string{
		"space":              "web 01",
		"leading space":      " web01",
		"trailing space":     "web01 ",
		"tab":                "web\t01",
		"newline":            "web01\n10.0.0.9 evil",
		"non-breaking space": "web 01",
		"NUL":                "a\x00b",
	}
	for name, v := range bad {
		err := NoWhitespace("name", v)
		if err == nil {
			t.Errorf("%s: NoWhitespace(%q): expected error, got nil", name, v)
			continue
		}
		if !strings.HasPrefix(err.Error(), "name: ") {
			t.Errorf("%s: error %q must be prefixed with the field name", name, err)
		}
	}
}

func TestNoControlCharsAllowsWhatNoWhitespaceRejects(t *testing.T) {
	// The two tiers differ exactly on plain whitespace: a comment or command
	// may contain spaces, a single column may not.
	if err := NoControlChars("comment", "deploy key for ci"); err != nil {
		t.Errorf("spaces must pass NoControlChars: %v", err)
	}
	if err := NoWhitespace("enc", "ssh rsa"); err == nil {
		t.Error("spaces must fail NoWhitespace")
	}
}
