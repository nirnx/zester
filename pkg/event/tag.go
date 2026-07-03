package event

import (
	"fmt"
	"strings"
)

// DottedTag maps a slash tag to its dotted NATS-subject form.
// The mapping is 1:1 because ValidateTag bans '.' inside tag segments.
// Example: DottedTag("myco/deploy/finished") -> "myco.deploy.finished"
func DottedTag(slashTag string) string {
	return strings.ReplaceAll(slashTag, "/", ".")
}

// SlashTag maps a dotted subject tag back to its slash form (the inverse
// of DottedTag).
// Example: SlashTag("myco.deploy.finished") -> "myco/deploy/finished"
func SlashTag(dottedTag string) string {
	return strings.ReplaceAll(dottedTag, ".", "/")
}

// MatchKey returns the key reactor rules match against: "<origin>/<slashTag>".
// Including the origin (a peel ID, "_master", or "_admin") is the forgery
// posture — a compromised peel can never make its events match a
// "_master/..." rule, because it cannot publish under the _master origin.
// Example: MatchKey("_master", "enroll/pending/enr-1") -> "_master/enroll/pending/enr-1"
func MatchKey(origin, slashTag string) string {
	return origin + "/" + slashTag
}

// ValidateTag checks a slash tag for publishability: it must be non-empty,
// every '/'-separated segment must be non-empty, and segments may only
// contain [a-zA-Z0-9_-]. This bans '.', '*', '>', whitespace, and any
// other character that would break the 1:1 dots<->slashes subject mapping
// or inject NATS wildcards. Unlike origins, tags may lead with '_'.
func ValidateTag(slashTag string) error {
	if slashTag == "" {
		return fmt.Errorf("event: validate tag: empty tag")
	}
	for _, seg := range strings.Split(slashTag, "/") {
		if seg == "" {
			return fmt.Errorf("event: validate tag %q: empty segment", slashTag)
		}
		for _, r := range seg {
			if !isTagRune(r) {
				return fmt.Errorf("event: validate tag %q: invalid character %q in segment %q (allowed: a-z A-Z 0-9 _ -)", slashTag, r, seg)
			}
		}
	}
	return nil
}

// isTagRune reports whether r is allowed inside a tag segment.
func isTagRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '_' || r == '-':
		return true
	}
	return false
}
