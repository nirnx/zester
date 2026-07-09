// Package reactor implements Zester's Salt-style reactor: a master-side
// engine that consumes durable events from the "events" JetStream stream,
// matches them against operator-written rules, renders Jinja reaction files,
// and executes typed actions (job dispatch, enrollment transitions, derived
// events, logging).
//
// Identity is always the NATS-permission-enforced subject token, never the
// payload: rules match against "<origin>/<slashTag>" keys (pkg/event
// MatchKey), so a compromised peel can never make its events match a
// "_master/..." rule. At-least-once delivery is made exactly-once at the
// side-effect layer via deterministic, content-addressed reaction JIDs
// (DeriveJID) and derived-event IDs (DeriveChainID).
package reactor

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nirnx/zester/pkg/enroll"
)

const (
	// FilePrefix is the KV key prefix under which reactor files live in the
	// reactor-files bucket (bus.BucketReactorFiles). The masterd publisher
	// maps the local reactor dir (default /data/reactor) onto this prefix.
	FilePrefix = "reactor/"

	// TopKey is the KV key of the reactor top file: an ordered, plain-YAML
	// (never templated) list of match-key globs mapped to reaction refs.
	TopKey = FilePrefix + "top.zy"
)

// Rule is one compiled (match-glob, reaction-ref) pair. A top-file entry with
// N reaction refs expands into N Rules sharing the entry's pattern and
// options; the Ref is the rule identity used for metrics, throttling, the
// circuit breaker, provenance metadata, and JID derivation.
type Rule struct {
	// Pattern is the fnmatch glob matched against "<origin>/<slashTag>"
	// keys. Salt-faithful semantics: '*' crosses '/', '?' matches one
	// character, '[seq]'/'[!seq]' are character classes.
	Pattern string

	// Ref is the dotted reaction-file reference (e.g.
	// "reactor.restart_service" -> KV key "reactor/restart_service.zy").
	Ref string

	// Throttle is the per-(rule,source) refractory period; 0 means the
	// engine default applies.
	Throttle time.Duration

	re *regexp.Regexp
}

// Matches reports whether the rule's pattern matches the given match key.
func (r *Rule) Matches(key string) bool {
	return r.re != nil && r.re.MatchString(key)
}

// RuleSet is an immutable snapshot of compiled rules plus the reaction-file
// contents they reference, swapped atomically by the Loader. Never mutate a
// published RuleSet.
type RuleSet struct {
	// Rules are the compiled rules in top-file order.
	Rules []Rule

	// Files maps KV keys (e.g. "reactor/restart_service.zy") to raw file
	// bytes from the same manifest-verified bucket snapshot as the rules.
	Files map[string][]byte
}

// Match returns all rules whose pattern matches key, in file order
// (Salt semantics: every matching entry fires).
func (rs *RuleSet) Match(key string) []Rule {
	if rs == nil {
		return nil
	}
	var matched []Rule
	for _, r := range rs.Rules {
		if r.Matches(key) {
			matched = append(matched, r)
		}
	}
	return matched
}

// File resolves a dotted reaction ref to its raw file bytes from the
// snapshot.
func (rs *RuleSet) File(ref string) ([]byte, bool) {
	if rs == nil || rs.Files == nil {
		return nil, false
	}
	data, ok := rs.Files[RefPath(ref)]
	return data, ok
}

// RefPath maps a dotted reaction ref to its KV key / relative path:
// "reactor.restart_service" -> "reactor/restart_service.zy".
func RefPath(ref string) string {
	return strings.ReplaceAll(ref, ".", "/") + ".zy"
}

// ValidateRef checks a dotted reaction ref: non-empty dot-separated segments
// of [a-zA-Z0-9_-] only, so the derived path can never escape the bucket
// namespace.
func ValidateRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("reactor: empty reaction ref")
	}
	for _, seg := range strings.Split(ref, ".") {
		if seg == "" {
			return fmt.Errorf("reactor: reaction ref %q: empty segment", ref)
		}
		for _, r := range seg {
			ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
			if !ok {
				return fmt.Errorf("reactor: reaction ref %q: invalid character %q (allowed: a-z A-Z 0-9 _ -)", ref, r)
			}
		}
	}
	return nil
}

// globCache caches compiled fnmatch patterns; patterns are operator-authored
// and bounded by the rule set, so an unbounded cache is safe.
var globCache sync.Map // pattern string -> *regexp.Regexp

// NormalizeMatchKeyOrigin applies the peel-ID dot encoding ('.' -> '_',
// enroll.SanitizeGlobDots) to the ORIGIN segment of a match key or match-key
// glob — the text before the first '/'. The first segment of a match key is
// always an origin (a peel ID, _master, or _admin), never a tag, and origin
// tokens can never contain '.', so a dotted origin pattern is dead as written
// — normalizing it only adds the matches the author meant
// ("web01.pl/service/*" matches the real key "web01_pl/service/nginx").
//
// Everything after the first '/' is tag territory and is deliberately left
// alone: tag segments MAY legitimately contain '_', so rewriting a dotted tag
// (probably a mistyped "deploy.done" for "deploy/done") could silently match
// an unrelated literal "deploy_done" tag. Patterns with no '/' are returned
// unchanged for the same reason.
func NormalizeMatchKeyOrigin(pattern string) string {
	idx := strings.IndexByte(pattern, '/')
	if idx < 0 {
		return pattern
	}
	return enroll.SanitizeGlobDots(pattern[:idx]) + pattern[idx:]
}

// PatternTagHasDot reports whether a match-key glob contains a '.' in TAG
// territory (after the first '/'). Such a pattern can never match a live key
// — tags cannot contain dots (event.ValidateTag) — so it is almost certainly
// a dotted peel id written where its '_' wire form is needed (beacon tags
// embed peel ids: "<origin>/beacon/<peelID>/<name>") or a mistyped '/'.
// Surfaced as a loader lint; never a load failure.
func PatternTagHasDot(pattern string) bool {
	idx := strings.IndexByte(pattern, '/')
	return idx >= 0 && strings.Contains(pattern[idx+1:], ".")
}

// CompileMatchGlob compiles an fnmatch pattern (Salt semantics: '*' crosses
// '/') into an anchored regexp, cached per pattern. The pattern's origin
// segment is dot-normalized first (NormalizeMatchKeyOrigin), so rules and
// watch filters written with the human dotted-hostname form match the wire
// origin tokens.
func CompileMatchGlob(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, fmt.Errorf("reactor: empty match glob")
	}
	if cached, ok := globCache.Load(pattern); ok {
		return cached.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile(translateFnmatch(NormalizeMatchKeyOrigin(pattern)))
	if err != nil {
		return nil, fmt.Errorf("reactor: compile match glob %q: %w", pattern, err)
	}
	globCache.Store(pattern, re)
	return re, nil
}

// MatchGlob reports whether s matches the fnmatch pattern. Invalid patterns
// never match.
func MatchGlob(pattern, s string) bool {
	re, err := CompileMatchGlob(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(s)
}

// translateFnmatch converts a Python-fnmatch-style pattern to an anchored
// regexp source. '*' -> ".*" (crosses '/'), '?' -> ".", "[seq]"/"[!seq]"
// character classes pass through ('!' becomes '^'); everything else is
// quoted. An unterminated '[' is treated as a literal bracket, matching
// Python's fnmatch.translate.
func translateFnmatch(pattern string) string {
	var b strings.Builder
	b.WriteString(`\A`)
	for i := 0; i < len(pattern); {
		c := pattern[i]
		i++
		switch c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '[':
			j := i
			if j < len(pattern) && pattern[j] == '!' {
				j++
			}
			if j < len(pattern) && pattern[j] == ']' {
				j++
			}
			for j < len(pattern) && pattern[j] != ']' {
				j++
			}
			if j >= len(pattern) {
				b.WriteString(`\[`)
				continue
			}
			stuff := strings.ReplaceAll(pattern[i:j], `\`, `\\`)
			switch {
			case strings.HasPrefix(stuff, "!"):
				stuff = "^" + stuff[1:]
			case strings.HasPrefix(stuff, "^"):
				stuff = `\` + stuff
			}
			b.WriteString("[")
			b.WriteString(stuff)
			b.WriteString("]")
			i = j + 1
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString(`\z`)
	return b.String()
}

// topDocument is the YAML shape of reactor/top.zy.
type topDocument struct {
	Reactor []map[string]yaml.Node `yaml:"reactor"`
}

// ruleOptions is the map-with-options entry form.
type ruleOptions struct {
	React    []string  `yaml:"react"`
	Throttle yaml.Node `yaml:"throttle"`
}

// ParseTopFile parses reactor/top.zy: plain YAML (never templated), an
// ordered list of single-key entries mapping a match-key glob to either a
// bare list of dotted reaction refs or a map with options:
//
//	reactor:
//	  - 'web-*/beacon/*/service/*':
//	      react:
//	        - reactor.restart_service
//	      throttle: 30s
//	  - '_master/enroll/pending/*':
//	      - reactor.autoapprove
//
// Every matching entry fires, in file order. Each ref expands into its own
// Rule sharing the entry's pattern and options.
func ParseTopFile(data []byte) ([]Rule, error) {
	var doc topDocument
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("reactor: parse top file: %w", err)
	}

	var rules []Rule
	for i, entry := range doc.Reactor {
		if len(entry) != 1 {
			return nil, fmt.Errorf("reactor: top file entry %d: want exactly one match-glob key, got %d", i, len(entry))
		}
		for pattern, node := range entry {
			entryRules, err := parseTopEntry(pattern, node)
			if err != nil {
				return nil, fmt.Errorf("reactor: top file entry %d (%q): %w", i, pattern, err)
			}
			rules = append(rules, entryRules...)
		}
	}
	return rules, nil
}

// parseTopEntry expands one top-file entry into Rules.
func parseTopEntry(pattern string, node yaml.Node) ([]Rule, error) {
	re, err := CompileMatchGlob(pattern)
	if err != nil {
		return nil, err
	}

	var refs []string
	var throttle time.Duration

	switch node.Kind {
	case yaml.SequenceNode:
		// Bare-list form: a sequence of dotted refs.
		if err := node.Decode(&refs); err != nil {
			return nil, fmt.Errorf("decode reaction list: %w", err)
		}
	case yaml.MappingNode:
		// Options form.
		for k := 0; k+1 < len(node.Content); k += 2 {
			switch key := node.Content[k].Value; key {
			case "react", "throttle":
			default:
				return nil, fmt.Errorf("unknown option %q (allowed: react, throttle)", key)
			}
		}
		var opts ruleOptions
		if err := node.Decode(&opts); err != nil {
			return nil, fmt.Errorf("decode options: %w", err)
		}
		refs = opts.React
		if !opts.Throttle.IsZero() {
			var raw any
			if err := opts.Throttle.Decode(&raw); err != nil {
				return nil, fmt.Errorf("decode throttle: %w", err)
			}
			throttle, err = parseFlexDuration(raw)
			if err != nil {
				return nil, fmt.Errorf("throttle: %w", err)
			}
			if throttle < 0 {
				return nil, fmt.Errorf("throttle: negative duration %s", throttle)
			}
		}
	default:
		return nil, fmt.Errorf("want a reaction list or an options map")
	}

	if len(refs) == 0 {
		return nil, fmt.Errorf("no reaction refs")
	}

	rules := make([]Rule, 0, len(refs))
	for _, ref := range refs {
		if err := ValidateRef(ref); err != nil {
			return nil, err
		}
		rules = append(rules, Rule{Pattern: pattern, Ref: ref, Throttle: throttle, re: re})
	}
	return rules, nil
}

// parseFlexDuration coerces a YAML scalar into a duration: strings parse via
// time.ParseDuration, numbers are seconds (Salt convention).
func parseFlexDuration(v any) (time.Duration, error) {
	switch t := v.(type) {
	case string:
		d, err := time.ParseDuration(t)
		if err != nil {
			return 0, fmt.Errorf("reactor: parse duration %q: %w", t, err)
		}
		return d, nil
	case int:
		return time.Duration(t) * time.Second, nil
	case int64:
		return time.Duration(t) * time.Second, nil
	case float64:
		return time.Duration(t * float64(time.Second)), nil
	default:
		return 0, fmt.Errorf("reactor: duration must be a string or number of seconds, got %T", v)
	}
}
