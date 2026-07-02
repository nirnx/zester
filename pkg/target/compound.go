package target

import (
	"fmt"
	"strings"
)

// CompoundMatcher evaluates compound targeting expressions that combine
// multiple matchers with boolean operators (and, or, not).
//
// Grammar:
//
//	expr     = orExpr
//	orExpr   = andExpr ("or" andExpr)*
//	andExpr  = notExpr ("and" notExpr)*
//	notExpr  = "not" notExpr | primary
//	primary  = "(" expr ")" | matcher
//	matcher  = prefixed | glob
//	prefixed = ("G@" | "I@" | "E@" | "L@") value
//	glob     = value (default: glob match on peel ID)
type CompoundMatcher struct {
	root     compoundNode
	original string
}

// compoundNode is a node in the compound expression tree.
type compoundNode interface {
	eval(peelID string, facts map[string]any) bool
}

type andNode struct{ left, right compoundNode }
type orNode struct{ left, right compoundNode }
type notNode struct{ child compoundNode }
type matcherNode struct{ m Matcher }

func (n *andNode) eval(id string, f map[string]any) bool {
	return n.left.eval(id, f) && n.right.eval(id, f)
}
func (n *orNode) eval(id string, f map[string]any) bool {
	return n.left.eval(id, f) || n.right.eval(id, f)
}
func (n *notNode) eval(id string, f map[string]any) bool {
	return !n.child.eval(id, f)
}
func (n *matcherNode) eval(id string, f map[string]any) bool {
	return n.m.Match(id, f)
}

// ParseCompound parses a compound targeting expression and returns a Matcher.
func ParseCompound(expr string) (*CompoundMatcher, error) {
	p := &parser{tokens: tokenize(expr), pos: 0}
	node, err := p.parseOr()
	if err != nil {
		return nil, fmt.Errorf("target: parse compound %q: %w", expr, err)
	}
	if p.pos < len(p.tokens) {
		return nil, fmt.Errorf("target: unexpected token %q at position %d in %q", p.tokens[p.pos], p.pos, expr)
	}
	return &CompoundMatcher{root: node, original: expr}, nil
}

func (m *CompoundMatcher) Match(peelID string, facts map[string]any) bool {
	return m.root.eval(peelID, facts)
}

func (m *CompoundMatcher) String() string   { return m.original }
func (m *CompoundMatcher) Type() TargetType { return Compound }

// tokenize splits a compound expression into tokens, preserving prefixed
// matchers (G@os:ubuntu) as single tokens.
func tokenize(expr string) []string {
	var tokens []string
	var current strings.Builder

	flush := func() {
		s := strings.TrimSpace(current.String())
		if s != "" {
			tokens = append(tokens, s)
		}
		current.Reset()
	}

	runes := []rune(expr)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		switch {
		case ch == '(' || ch == ')':
			flush()
			tokens = append(tokens, string(ch))
		case ch == ' ' || ch == '\t':
			flush()
		default:
			current.WriteRune(ch)
		}
	}
	flush()

	return tokens
}

// parser is a recursive-descent parser for compound expressions.
type parser struct {
	tokens []string
	pos    int
}

func (p *parser) peek() string {
	if p.pos >= len(p.tokens) {
		return ""
	}
	return p.tokens[p.pos]
}

func (p *parser) advance() string {
	t := p.peek()
	p.pos++
	return t
}

func (p *parser) parseOr() (compoundNode, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for strings.EqualFold(p.peek(), "or") {
		p.advance()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &orNode{left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseAnd() (compoundNode, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for strings.EqualFold(p.peek(), "and") {
		p.advance()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = &andNode{left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseNot() (compoundNode, error) {
	if strings.EqualFold(p.peek(), "not") {
		p.advance()
		child, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &notNode{child: child}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (compoundNode, error) {
	tok := p.peek()
	if tok == "" {
		return nil, fmt.Errorf("unexpected end of expression")
	}

	if tok == "(" {
		p.advance()
		node, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.advance() != ")" {
			return nil, fmt.Errorf("expected closing parenthesis")
		}
		return node, nil
	}

	return p.parseMatcher()
}

func (p *parser) parseMatcher() (compoundNode, error) {
	tok := p.advance()
	if tok == "" {
		return nil, fmt.Errorf("expected matcher expression")
	}

	var m Matcher
	var err error

	switch {
	case strings.HasPrefix(tok, "G@") || strings.HasPrefix(tok, "I@"):
		m, err = NewFactMatcher(tok)
	case strings.HasPrefix(tok, "E@"):
		m, err = NewPCREMatcher(tok)
	case strings.HasPrefix(tok, "L@"):
		m, err = NewListMatcher(tok)
	default:
		// Default: treat as glob pattern.
		m, err = NewGlobMatcher(tok)
	}

	if err != nil {
		return nil, err
	}
	return &matcherNode{m: m}, nil
}
