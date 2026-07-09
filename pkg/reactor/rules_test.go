package reactor

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeMatchKeyOrigin(t *testing.T) {
	cases := []struct{ in, want string }{
		// Origin segment (before the first '/') gets the '.' -> '_' encoding.
		{"web01.pl/service/nginx", "web01_pl/service/nginx"},
		{"*.oxm/service/*", "*_oxm/service/*"},
		{"devops-hetzner.oxm/*", "devops-hetzner_oxm/*"},
		// Reserved origins and clean origins are unchanged.
		{"_master/enroll/pending/*", "_master/enroll/pending/*"},
		{"web-01/deploy/finished", "web-01/deploy/finished"},
		// Tag territory is NEVER touched: tags may legitimately contain '_',
		// so a dotted tag (probably a mistyped slash) must not be rewritten.
		{"web-01/deploy.done", "web-01/deploy.done"},
		{"*/myco.deploy/finished", "*/myco.deploy/finished"},
		// No '/' at all: could be tag text via '*'-crossing — unchanged.
		{"*", "*"},
		{"deploy.done", "deploy.done"},
	}
	for _, c := range cases {
		if got := NormalizeMatchKeyOrigin(c.in); got != c.want {
			t.Errorf("NormalizeMatchKeyOrigin(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPatternTagHasDot(t *testing.T) {
	cases := []struct {
		pattern string
		want    bool
	}{
		{"*/beacon/db01.example.com/service", true}, // dotted peel id in tag territory: dead rule
		{"web-01/deploy.done", true},                // mistyped dotted tag: dead rule
		{"web01.pl/service/*", false},               // dotted ORIGIN is fine (normalized)
		{"*/beacon/db01_example_com/service", false},
		{"_master/enroll/pending/*", false},
		{"deploy.done", false}, // no '/', not tag territory
		{"*", false},
	}
	for _, c := range cases {
		if got := PatternTagHasDot(c.pattern); got != c.want {
			t.Errorf("PatternTagHasDot(%q) = %v, want %v", c.pattern, got, c.want)
		}
	}
}

func TestMatchGlobDottedOrigin(t *testing.T) {
	// A rule written with the human dotted-hostname origin matches the wire
	// match key (origin token has '.' encoded as '_').
	cases := []struct {
		pattern, key string
		want         bool
	}{
		{"web01.pl/service/*", "web01_pl/service/nginx", true},
		{"*.oxm/service/*", "devops-hetzner_oxm/service/nginx", true},
		{"web01.pl/service/*", "web01-pl/service/nginx", false}, // hyphenated host stays distinct
		{"_master/enroll/pending/*", "_master/enroll/pending/enr-1", true},
		// A dotted TAG is not normalized: it does not match an underscored tag.
		{"*/deploy.done", "web-01/deploy_done", false},
	}
	for _, c := range cases {
		if got := MatchGlob(c.pattern, c.key); got != c.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", c.pattern, c.key, got, c.want)
		}
	}
}

func TestParseTopFileBareListForm(t *testing.T) {
	top := `
reactor:
  - 'myco/deploy/finished':
      - reactor.announce
      - reactor.notify
`
	rules, err := ParseTopFile([]byte(top))
	if err != nil {
		t.Fatalf("ParseTopFile: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("want 2 rules, got %d", len(rules))
	}
	if rules[0].Ref != "reactor.announce" || rules[1].Ref != "reactor.notify" {
		t.Errorf("refs out of order: %q, %q", rules[0].Ref, rules[1].Ref)
	}
	for _, r := range rules {
		if r.Pattern != "myco/deploy/finished" {
			t.Errorf("pattern: got %q", r.Pattern)
		}
		if r.Throttle != 0 {
			t.Errorf("bare-list rule must have zero throttle, got %s", r.Throttle)
		}
	}
}

func TestParseTopFileOptionsForm(t *testing.T) {
	top := `
reactor:
  - '*/beacon/*/service/*':
      react:
        - reactor.restart_service
      throttle: 30s
`
	rules, err := ParseTopFile([]byte(top))
	if err != nil {
		t.Fatalf("ParseTopFile: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("want 1 rule, got %d", len(rules))
	}
	if rules[0].Throttle != 30*time.Second {
		t.Errorf("throttle: got %s, want 30s", rules[0].Throttle)
	}
	if rules[0].Ref != "reactor.restart_service" {
		t.Errorf("ref: got %q", rules[0].Ref)
	}
}

func TestParseTopFileThrottleNumberIsSeconds(t *testing.T) {
	top := `
reactor:
  - 'a/*':
      react: [reactor.x]
      throttle: 15
`
	rules, err := ParseTopFile([]byte(top))
	if err != nil {
		t.Fatalf("ParseTopFile: %v", err)
	}
	if rules[0].Throttle != 15*time.Second {
		t.Errorf("throttle: got %s, want 15s", rules[0].Throttle)
	}
}

func TestParseTopFileOrderPreserved(t *testing.T) {
	top := `
reactor:
  - 'z/*':
      - reactor.z
  - 'a/*':
      - reactor.a
  - '*':
      - reactor.all
`
	rules, err := ParseTopFile([]byte(top))
	if err != nil {
		t.Fatalf("ParseTopFile: %v", err)
	}
	want := []string{"reactor.z", "reactor.a", "reactor.all"}
	for i, w := range want {
		if rules[i].Ref != w {
			t.Errorf("rules[%d].Ref = %q, want %q", i, rules[i].Ref, w)
		}
	}
}

func TestParseTopFileErrors(t *testing.T) {
	tests := []struct {
		name, top, errPart string
	}{
		{"bad yaml", "reactor:\n  - 'a': [\n", "parse top file"},
		{"two keys per entry", "reactor:\n  - 'a': [reactor.x]\n    'b': [reactor.y]\n", "exactly one match-glob key"},
		{"no refs", "reactor:\n  - 'a':\n      react: []\n", "no reaction refs"},
		{"unknown option", "reactor:\n  - 'a':\n      react: [reactor.x]\n      bogus: 1\n", "unknown option"},
		{"bad throttle", "reactor:\n  - 'a':\n      react: [reactor.x]\n      throttle: soon\n", "throttle"},
		{"negative throttle", "reactor:\n  - 'a':\n      react: [reactor.x]\n      throttle: -5s\n", "negative"},
		{"scalar entry body", "reactor:\n  - 'a': reactor.x\n", "reaction list or an options map"},
		{"bad ref slash", "reactor:\n  - 'a': [reactor/x]\n", "invalid character"},
		{"bad ref empty segment", "reactor:\n  - 'a': [reactor..x]\n", "empty segment"},
		{"bad glob class", "reactor:\n  - 'a[z-a]': [reactor.x]\n", "compile match glob"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseTopFile([]byte(tt.top))
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tt.errPart)
			}
			if !strings.Contains(err.Error(), tt.errPart) {
				t.Errorf("error %q does not contain %q", err, tt.errPart)
			}
		})
	}
}

func TestMatchGlobFnmatchSemantics(t *testing.T) {
	tests := []struct {
		pattern, key string
		want         bool
	}{
		// '*' crosses '/' (Salt-faithful fnmatch).
		{"beacon/*", "beacon/web-01/service/nginx", true},
		{"*", "anything/at/all", true},
		{"*/beacon/*/service/*", "web-01/beacon/web-01/service/nginx", true},
		{"_master/enroll/pending/*", "_master/enroll/pending/enr-1", true},
		// Anchoring: no partial matches.
		{"b/c", "a/b/c", false},
		{"a/b", "a/b/c", false},
		// '?' matches exactly one character.
		{"web-0?/ping", "web-01/ping", true},
		{"web-0?/ping", "web-012/ping", false},
		// Character classes.
		{"web-0[12]/ping", "web-01/ping", true},
		{"web-0[12]/ping", "web-03/ping", false},
		{"web-0[!12]/ping", "web-03/ping", true},
		{"web-0[!12]/ping", "web-01/ping", false},
		// Regex metacharacters are literal. Dots are exercised in TAG position
		// — an origin-position '.' is rewritten by NormalizeMatchKeyOrigin
		// (real origins are subject tokens and can never contain '.').
		{"a/b.c", "a/b.c", true},
		{"a/b.c", "a/bxc", false},
		{"a.b/c", "a_b/c", true},  // dotted origin matches the encoded token…
		{"a.b/c", "a.b/c", false}, // …and a dotted-origin key cannot exist on the wire
		{"a+b", "a+b", true},
		{"a+b", "aab", false},
		// Unterminated '[' is a literal bracket.
		{"a[b", "a[b", true},
		{"a[b", "ab", false},
		// A peel spoofing "_master" inside its TAG cannot reach a
		// _master-prefixed rule: the key carries the true origin first.
		{"_master/enroll/*", "evil/_master/enroll/pending/enr-1", false},
	}
	for _, tt := range tests {
		if got := MatchGlob(tt.pattern, tt.key); got != tt.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", tt.pattern, tt.key, got, tt.want)
		}
	}
}

func TestRuleSetMatchAllInOrder(t *testing.T) {
	top := `
reactor:
  - 'web-*/deploy/*':
      - reactor.first
  - 'nope/*':
      - reactor.never
  - '*':
      - reactor.catchall
`
	rules, err := ParseTopFile([]byte(top))
	if err != nil {
		t.Fatalf("ParseTopFile: %v", err)
	}
	rs := &RuleSet{Rules: rules}

	matched := rs.Match("web-01/deploy/finished")
	if len(matched) != 2 {
		t.Fatalf("want 2 matches, got %d", len(matched))
	}
	if matched[0].Ref != "reactor.first" || matched[1].Ref != "reactor.catchall" {
		t.Errorf("match order wrong: %q, %q", matched[0].Ref, matched[1].Ref)
	}
	if got := rs.Match("zzz"); len(got) != 1 || got[0].Ref != "reactor.catchall" {
		t.Errorf("catchall only: got %v", got)
	}

	var nilSet *RuleSet
	if got := nilSet.Match("x"); got != nil {
		t.Errorf("nil RuleSet must match nothing, got %v", got)
	}
}

func TestRefPath(t *testing.T) {
	tests := []struct{ ref, want string }{
		{"reactor.restart_service", "reactor/restart_service.zy"},
		{"reactor.sub.deep", "reactor/sub/deep.zy"},
		{"top-level", "top-level.zy"},
	}
	for _, tt := range tests {
		if got := RefPath(tt.ref); got != tt.want {
			t.Errorf("RefPath(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestRuleSetFile(t *testing.T) {
	rs := &RuleSet{Files: map[string][]byte{"reactor/x.zy": []byte("hi")}}
	if data, ok := rs.File("reactor.x"); !ok || string(data) != "hi" {
		t.Errorf("File(reactor.x) = %q, %v", data, ok)
	}
	if _, ok := rs.File("reactor.missing"); ok {
		t.Error("missing ref must not resolve")
	}
}

func TestValidateRef(t *testing.T) {
	for _, ok := range []string{"reactor.x", "a.b-c.d_e", "Single"} {
		if err := ValidateRef(ok); err != nil {
			t.Errorf("ValidateRef(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "a..b", ".a", "a.", "a/b", "a b", "a.*", "a.>"} {
		if err := ValidateRef(bad); err == nil {
			t.Errorf("ValidateRef(%q) should fail", bad)
		}
	}
}
