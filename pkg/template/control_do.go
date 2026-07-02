package template

import (
	"fmt"

	"github.com/nikolalohinski/gonja/v2/exec"
	"github.com/nikolalohinski/gonja/v2/nodes"
	"github.com/nikolalohinski/gonja/v2/parser"
	"github.com/nikolalohinski/gonja/v2/tokens"
)

// doControlStructure implements {% do expr %}. It evaluates an expression
// for its side effects and discards the result. This enables patterns like
// {% do list.append(1) %} which Salt templates use extensively.
type doControlStructure struct {
	location   *tokens.Token
	expression nodes.Expression
}

func (cs *doControlStructure) Position() *tokens.Token {
	return cs.location
}

func (cs *doControlStructure) String() string {
	t := cs.Position()
	return fmt.Sprintf("DoControlStructure(Line=%d Col=%d)", t.Line, t.Col)
}

func (cs *doControlStructure) Execute(r *exec.Renderer, tag *nodes.ControlStructureBlock) error {
	value := r.Eval(cs.expression)
	if value.IsError() {
		return fmt.Errorf("do: %w", value)
	}
	return nil
}

func doParser(p *parser.Parser, args *parser.Parser) (nodes.ControlStructure, error) {
	cs := &doControlStructure{
		location: p.Current(),
	}

	expr, err := args.ParseExpression()
	if err != nil {
		return nil, fmt.Errorf("do: expected expression: %w", err)
	}
	cs.expression = expr

	if !args.End() {
		return nil, args.Error("do: unexpected extra arguments", args.Current())
	}

	return cs, nil
}
