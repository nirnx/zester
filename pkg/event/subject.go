package event

import (
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/bus"
)

// Kind classifies an event subject by its token layout.
type Kind string

const (
	// KindBeacon is a peel beacon event:
	// zester.event.<peelID>.beacon.<name>[.<extra...>]
	KindBeacon Kind = "beacon"

	// KindSend is a custom peel event from the event.send module:
	// zester.event.<peelID>.send.<t1>...<tn>
	KindSend Kind = "send"

	// KindMaster is a master-synthesized event:
	// zester.event._master.<t1>...<tn>
	KindMaster Kind = "master"

	// KindAdmin is an operator-sent event (zester event send):
	// zester.event._admin.send.<t1>...<tn>
	KindAdmin Kind = "admin"
)

// minSubjectTokens is the shortest well-formed event subject:
// zester.event._master.<t1>.
const minSubjectTokens = 4

// ParseSubject derives the authoritative event identity from a NATS subject
// under zester.event.>. It returns the origin (subject token[2]: a peel ID,
// bus.OriginMaster, or bus.OriginAdmin), the Salt-style slash tag, and the
// subject kind. The subject — not the payload — is the trust anchor: the
// NATS server enforces which origins a publisher may claim.
//
// Recognized shapes:
//
//	zester.event.<peelID>.beacon.<name>[.<extra...>] -> tag "beacon/<peelID>/<name>[/...]"
//	zester.event.<peelID>.send.<t1>...<tn>           -> tag "t1/.../tn"
//	zester.event._master.<t1>...<tn>                 -> tag "t1/.../tn"
//	zester.event._admin.send.<t1>...<tn>             -> tag "t1/.../tn"
//
// Subjects with fewer than 4 tokens, empty tokens, wildcard tokens, an
// unrecognized shape, or a reserved (leading '_') origin other than
// _master/_admin are rejected.
func ParseSubject(subject string) (origin, slashTag string, kind Kind, err error) {
	tokens := strings.Split(subject, ".")
	if len(tokens) < minSubjectTokens {
		return "", "", "", fmt.Errorf("event: parse subject %q: need at least %d tokens, got %d", subject, minSubjectTokens, len(tokens))
	}
	for _, tok := range tokens {
		switch tok {
		case "":
			return "", "", "", fmt.Errorf("event: parse subject %q: empty token", subject)
		case bus.WildcardOne, bus.WildcardMany:
			return "", "", "", fmt.Errorf("event: parse subject %q: wildcard token %q", subject, tok)
		}
	}
	if tokens[0]+"."+tokens[1] != bus.SubjectEvent {
		return "", "", "", fmt.Errorf("event: parse subject %q: not under %s", subject, bus.SubjectEvent)
	}

	origin = tokens[2]
	switch origin {
	case bus.OriginMaster:
		// zester.event._master.<t1>... — everything after the origin is the tag.
		return origin, strings.Join(tokens[3:], "/"), KindMaster, nil

	case bus.OriginAdmin:
		// zester.event._admin.send.<t1>...
		if tokens[3] != bus.SubjectEventSend {
			return "", "", "", fmt.Errorf("event: parse subject %q: admin events must use the %q sub-token, got %q", subject, bus.SubjectEventSend, tokens[3])
		}
		if len(tokens) < 5 {
			return "", "", "", fmt.Errorf("event: parse subject %q: admin send subject has no tag tokens", subject)
		}
		return origin, strings.Join(tokens[4:], "/"), KindAdmin, nil

	default:
		if strings.HasPrefix(origin, "_") {
			// _-prefixed origins are reserved for trusted sources; enrollment
			// bans them as peel IDs, so any other _-origin is malformed.
			return "", "", "", fmt.Errorf("event: parse subject %q: reserved origin %q", subject, origin)
		}
		switch tokens[3] {
		case bus.SubjectBeacon:
			if len(tokens) < 5 {
				return "", "", "", fmt.Errorf("event: parse subject %q: beacon subject has no name token", subject)
			}
			// Salt shape: beacon/<peelID>/<name>[/...].
			parts := append([]string{bus.SubjectBeacon, origin}, tokens[4:]...)
			return origin, strings.Join(parts, "/"), KindBeacon, nil
		case bus.SubjectEventSend:
			if len(tokens) < 5 {
				return "", "", "", fmt.Errorf("event: parse subject %q: send subject has no tag tokens", subject)
			}
			return origin, strings.Join(tokens[4:], "/"), KindSend, nil
		default:
			return "", "", "", fmt.Errorf("event: parse subject %q: unknown peel event sub-token %q (want %q or %q)", subject, tokens[3], bus.SubjectBeacon, bus.SubjectEventSend)
		}
	}
}
