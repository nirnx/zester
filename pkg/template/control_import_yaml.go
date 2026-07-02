package template

import (
	"fmt"
	"io"

	"github.com/nikolalohinski/gonja/v2/exec"
	"github.com/nikolalohinski/gonja/v2/nodes"
	"github.com/nikolalohinski/gonja/v2/parser"
	"github.com/nikolalohinski/gonja/v2/tokens"
	"gopkg.in/yaml.v3"
)

// importYAMLControlStructure implements {% import_yaml 'path' as var %}.
// It reads a YAML file via the template loader and sets the parsed data
// as a variable in the template context, matching Salt's import_yaml tag.
type importYAMLControlStructure struct {
	location       *tokens.Token
	pathExpression nodes.Expression
	varName        string
}

func (cs *importYAMLControlStructure) Position() *tokens.Token {
	return cs.location
}

func (cs *importYAMLControlStructure) String() string {
	t := cs.Position()
	return fmt.Sprintf("ImportYAMLControlStructure(Line=%d Col=%d)", t.Line, t.Col)
}

func (cs *importYAMLControlStructure) Execute(r *exec.Renderer, tag *nodes.ControlStructureBlock) error {
	pathValue := r.Eval(cs.pathExpression)
	if pathValue.IsError() {
		return fmt.Errorf("import_yaml: unable to evaluate path: %w", pathValue)
	}

	path := pathValue.String()

	resolved, err := r.Loader.Resolve(path)
	if err != nil {
		return fmt.Errorf("import_yaml: resolve %q: %w", path, err)
	}

	reader, err := r.Loader.Read(resolved)
	if err != nil {
		return fmt.Errorf("import_yaml: read %q: %w", resolved, err)
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("import_yaml: read content %q: %w", resolved, err)
	}

	var parsed any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("import_yaml: parse %q: %w", resolved, err)
	}

	// Normalize yaml.v3 output to map[string]any for gonja compatibility.
	parsed = normalizeYAML(parsed)

	r.Environment.Context.Set(cs.varName, parsed)
	return nil
}

// normalizeYAML converts map[any]any (from yaml.v3) to map[string]any
// recursively, which is what gonja expects.
func normalizeYAML(v any) any {
	switch val := v.(type) {
	case map[string]any:
		for k, v := range val {
			val[k] = normalizeYAML(v)
		}
		return val
	case map[any]any:
		out := make(map[string]any, len(val))
		for k, v := range val {
			out[fmt.Sprintf("%v", k)] = normalizeYAML(v)
		}
		return out
	case []any:
		for i, v := range val {
			val[i] = normalizeYAML(v)
		}
		return val
	default:
		return v
	}
}

func importYAMLParser(p *parser.Parser, args *parser.Parser) (nodes.ControlStructure, error) {
	cs := &importYAMLControlStructure{
		location: p.Current(),
	}

	// Parse the path expression (e.g., 'defaults.yaml' or a variable)
	pathExpr, err := args.ParseExpression()
	if err != nil {
		return nil, fmt.Errorf("import_yaml: expected path expression: %w", err)
	}
	cs.pathExpression = pathExpr

	// Expect "as" keyword
	if args.MatchName("as") == nil {
		return nil, args.Error("import_yaml: expected 'as' keyword", args.Current())
	}

	// Parse variable name
	varToken := args.Match(tokens.Name)
	if varToken == nil {
		return nil, args.Error("import_yaml: expected variable name after 'as'", args.Current())
	}
	cs.varName = varToken.Val

	if !args.End() {
		return nil, args.Error("import_yaml: unexpected extra arguments", args.Current())
	}

	return cs, nil
}
