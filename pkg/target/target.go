// Package target implements the Zester targeting engine which resolves
// target expressions (glob, PCRE, fact-based, compound, list) into
// concrete peel IDs by querying the NATS KV facts bucket.
package target

import (
	"context"
	"fmt"
	"strings"
)

// TargetType identifies the kind of targeting expression.
type TargetType int

const (
	Glob     TargetType = iota // "web*"
	PCRE                       // "E@web-dc[12]-srv\d+"
	Fact                       // "G@os:ubuntu"
	Settings                   // "I@role:webserver"
	Compound                   // "web* and G@os:ubuntu and not E@.*-dev-.*"
	List                       // "L@web01,web02,web03"
)

func (t TargetType) String() string {
	switch t {
	case Glob:
		return "glob"
	case PCRE:
		return "pcre"
	case Fact:
		return "fact"
	case Settings:
		return "settings"
	case Compound:
		return "compound"
	case List:
		return "list"
	default:
		return "unknown"
	}
}

// Matcher matches peel IDs against a target expression.
type Matcher interface {
	// Match returns true if the given peel ID matches the target expression.
	// For matchers that need facts (Fact, Settings), the facts map is consulted.
	Match(peelID string, facts map[string]any) bool

	// String returns the original target expression.
	String() string

	// Type returns the target type of this matcher.
	Type() TargetType
}

// PeelLister provides the set of known peel IDs and their facts.
type PeelLister interface {
	// ListPeels returns all known peel IDs.
	ListPeels(ctx context.Context) ([]string, error)

	// GetFacts returns the facts map for a given peel ID.
	GetFacts(ctx context.Context, peelID string) (map[string]any, error)
}

// BulkPeelLister extends PeelLister with a single-call bulk facts load.
// Implementations that can efficiently retrieve all peel facts at once (e.g.,
// via KV WatchAll) should implement this interface to avoid N+1 KV reads
// during fact-based targeting.
type BulkPeelLister interface {
	PeelLister
	// ListPeelsWithFacts returns all known peel IDs mapped to their facts.
	ListPeelsWithFacts(ctx context.Context) (map[string]map[string]any, error)
}

// ExprResolver resolves a complete target expression in one call. Listers
// that implement it (e.g. ServiceLister, which delegates to the master-side
// resolve service) short-circuit Resolve: matching happens remotely and only
// the matched peel IDs are returned.
type ExprResolver interface {
	ResolveExpr(ctx context.Context, expr string, tt TargetType) ([]string, error)
}

// Resolve takes a target expression and type, and resolves it to a list
// of matching peel IDs by consulting the PeelLister.
//
// When the lister implements ExprResolver, the whole expression is delegated
// to it (remote resolution). Otherwise, when the target type requires facts
// (Fact, Settings, Compound) and the lister implements BulkPeelLister, all
// facts are loaded in a single bulk call instead of N individual GetFacts
// calls.
func Resolve(ctx context.Context, expr string, tt TargetType, lister PeelLister) ([]string, error) {
	if er, ok := lister.(ExprResolver); ok {
		return er.ResolveExpr(ctx, expr, tt)
	}

	matcher, err := NewMatcher(expr, tt)
	if err != nil {
		return nil, fmt.Errorf("target: create matcher: %w", err)
	}

	// Fast path: bulk-load all facts in a single call when possible.
	if needsFacts(tt) {
		if bulk, ok := lister.(BulkPeelLister); ok {
			return resolveBulk(ctx, matcher, bulk)
		}
	}

	peels, err := lister.ListPeels(ctx)
	if err != nil {
		return nil, fmt.Errorf("target: list peels: %w", err)
	}

	var matched []string
	for _, id := range peels {
		var facts map[string]any
		if needsFacts(tt) {
			facts, err = lister.GetFacts(ctx, id)
			if err != nil {
				continue // skip peels with unavailable facts
			}
		}
		if matcher.Match(id, facts) {
			matched = append(matched, id)
		}
	}
	return matched, nil
}

// resolveBulk loads all peel facts in one shot and matches in-memory.
func resolveBulk(ctx context.Context, matcher Matcher, bulk BulkPeelLister) ([]string, error) {
	allFacts, err := bulk.ListPeelsWithFacts(ctx)
	if err != nil {
		return nil, fmt.Errorf("target: bulk list peels: %w", err)
	}

	var matched []string
	for id, facts := range allFacts {
		if matcher.Match(id, facts) {
			matched = append(matched, id)
		}
	}
	return matched, nil
}

// NewMatcher creates a Matcher for the given expression and target type.
func NewMatcher(expr string, tt TargetType) (Matcher, error) {
	switch tt {
	case Glob:
		return NewGlobMatcher(expr)
	case PCRE:
		return NewPCREMatcher(expr)
	case Fact:
		return NewFactMatcher(expr)
	case Settings:
		return NewFactMatcher(expr) // same grammar, different KV bucket in practice
	case Compound:
		return ParseCompound(expr)
	case List:
		return NewListMatcher(expr)
	default:
		return nil, fmt.Errorf("target: unsupported target type: %s", tt)
	}
}

// DetectType inspects an expression string and returns the most likely
// TargetType. Prefixed expressions (E@, G@, I@, L@) are detected
// automatically; bare expressions default to Glob.
func DetectType(expr string) TargetType {
	if strings.HasPrefix(expr, "E@") {
		return PCRE
	}
	if strings.HasPrefix(expr, "G@") {
		return Fact
	}
	if strings.HasPrefix(expr, "I@") {
		return Settings
	}
	if strings.HasPrefix(expr, "L@") {
		return List
	}
	// Compound expressions contain "and", "or", "not" operators with
	// mixed prefix matchers.
	lower := strings.ToLower(expr)
	if strings.Contains(lower, " and ") || strings.Contains(lower, " or ") || strings.HasPrefix(lower, "not ") {
		return Compound
	}
	return Glob
}

func needsFacts(tt TargetType) bool {
	switch tt {
	case Fact, Settings, Compound:
		return true
	default:
		return false
	}
}
