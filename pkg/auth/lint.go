package auth

import (
	"fmt"
	"sort"
	"strings"
)

// This file implements an OFFLINE linter for NATS user permissions. It encodes
// the JetStream access patterns whose grants are easy to hand-enumerate
// incompletely — the class of bug where a component connects fine but a
// specific operation is silently denied at runtime (e.g. the object-store
// download that failed because the peel creds lacked
// $JS.FC.OBJ_update-binaries.>). The rules are heuristic but precise: they key
// off grants the creds DO carry (consumer-create on a stream) and flag the
// companion grants JetStream requires for that access pattern.

// LintSeverity classifies a lint finding.
type LintSeverity string

const (
	// LintError is a grant gap that will reliably break an operation the creds
	// are clearly meant to perform (e.g. an object-store download with no
	// flow-control publish grant).
	LintError LintSeverity = "error"
	// LintWarn is a grant gap that can break an operation under load /
	// backpressure (e.g. a KV watch's flow control) but often works otherwise.
	LintWarn LintSeverity = "warn"
)

// LintFinding is one detected grant gap.
type LintFinding struct {
	Severity LintSeverity
	Rule     string
	Message  string
}

// LintPermissions applies the JetStream access-pattern rules to a user's
// allow-publish and allow-subscribe subject lists and returns the findings
// (deterministically ordered). It is the testable core of `zester auth lint`.
func LintPermissions(allowPub, allowSub []string) []LintFinding {
	var findings []LintFinding

	// Rule: every stream the creds can create a consumer on needs a matching
	// flow-control publish grant. Ordered/push consumers — object-store
	// downloads and KV watches — require the client to publish flow-control
	// acks to $JS.FC.<stream>.>; without it the delivery stalls with a
	// permissions violation.
	for _, stream := range consumerStreams(allowPub) {
		fcProbe := "$JS.FC." + stream + ".cons.seq"
		if anyAllows(allowPub, fcProbe) {
			continue
		}
		sev := LintWarn
		detail := "ordered/push consumers (e.g. KV watches) can stall under backpressure"
		if strings.HasPrefix(stream, "OBJ_") {
			// Object-store Get always uses a flow-controlled ordered consumer
			// and reliably fails without the grant.
			sev = LintError
			detail = "object-store downloads reliably fail without it"
		}
		findings = append(findings, LintFinding{
			Severity: sev,
			Rule:     "jetstream-flow-control",
			Message: fmt.Sprintf(
				"consumer-create grant on stream %q but no publish grant for its flow-control subject $JS.FC.%s.> — %s",
				stream, stream, detail),
		})
	}

	// Rule: a creds set that creates consumers must be able to receive their
	// deliveries on an inbox.
	if len(consumerStreams(allowPub)) > 0 && !anyAllows(allowSub, "_INBOX.reply") {
		findings = append(findings, LintFinding{
			Severity: LintWarn,
			Rule:     "consumer-inbox",
			Message:  "creates consumers but has no _INBOX subscribe grant — push/ordered consumers deliver to an inbox subject",
		})
	}

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Severity != findings[j].Severity {
			return findings[i].Severity == LintError // errors first
		}
		return findings[i].Message < findings[j].Message
	})
	return findings
}

// LintUserClaims lints a decoded user JWT's permissions.
func LintUserClaims(userJWT string) ([]LintFinding, error) {
	uc, err := DecodeUserJWT(userJWT)
	if err != nil {
		return nil, err
	}
	return LintPermissions(uc.Permissions.Pub.Allow, uc.Permissions.Sub.Allow), nil
}

// LintCredsFile decodes the user JWT embedded in a NATS .creds file and lints
// its permissions.
func LintCredsFile(path string) ([]LintFinding, error) {
	cf, err := LoadCredsFile(path)
	if err != nil {
		return nil, err
	}
	return LintUserClaims(cf.JWT)
}

// consumerStreams returns the distinct JetStream stream names the creds can
// create a consumer on, extracted from $JS.API.CONSUMER.CREATE.<stream> and
// $JS.API.CONSUMER.DURABLE.CREATE.<stream>[...] grants. A wildcard stream
// token is skipped (no specific stream to check).
func consumerStreams(allowPub []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, g := range allowPub {
		tok := strings.Split(g, ".")
		for i := 0; i+1 < len(tok); i++ {
			if tok[i] != "CREATE" || i == 0 {
				continue
			}
			if prev := tok[i-1]; prev != "CONSUMER" && prev != "DURABLE" {
				continue
			}
			stream := tok[i+1]
			if stream == "" || stream == ">" || stream == "*" {
				continue
			}
			if _, dup := seen[stream]; dup {
				continue
			}
			seen[stream] = struct{}{}
			out = append(out, stream)
		}
	}
	sort.Strings(out)
	return out
}

// anyAllows reports whether any granted subject pattern matches the concrete
// subject under NATS wildcard semantics (`*` one token, `>` the tail).
func anyAllows(grants []string, subject string) bool {
	for _, g := range grants {
		if subjectAllows(g, subject) {
			return true
		}
	}
	return false
}

// subjectAllows reports whether a NATS subject pattern matches a concrete
// subject (`*` matches one token, `>` matches one-or-more trailing tokens).
func subjectAllows(pattern, subject string) bool {
	pt := strings.Split(pattern, ".")
	st := strings.Split(subject, ".")
	for i, p := range pt {
		if p == ">" {
			return i < len(st) // `>` must match at least one token
		}
		if i >= len(st) {
			return false
		}
		if p != "*" && p != st[i] {
			return false
		}
	}
	return len(pt) == len(st)
}
