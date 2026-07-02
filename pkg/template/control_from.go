package template

import (
	"fmt"
	"io"

	"github.com/nikolalohinski/gonja/v2/exec"
	"github.com/nikolalohinski/gonja/v2/nodes"
	"github.com/nikolalohinski/gonja/v2/parser"
	"github.com/nikolalohinski/gonja/v2/tokens"
)

// fromImportControlStructure implements {% from "file" import name1, name2 with context %}.
//
// This replaces gonja's built-in from/import which only supports macros.
// Salt templates use {% from "map.jinja" import map with context %} to share
// variables (set via {% set %}) between template files — a pattern gonja
// doesn't support natively. Our implementation actually executes the imported
// template and extracts named variables from its context, falling back to
// macros for backward compatibility.
type fromImportControlStructure struct {
	location           *tokens.Token
	filenameExpression nodes.Expression
	withContext        bool
	as                 map[string]string // alias -> original name
}

func (cs *fromImportControlStructure) Position() *tokens.Token {
	return cs.location
}

func (cs *fromImportControlStructure) String() string {
	t := cs.Position()
	return fmt.Sprintf("FromImportControlStructure(Line=%d Col=%d)", t.Line, t.Col)
}

func (cs *fromImportControlStructure) Execute(r *exec.Renderer, tag *nodes.ControlStructureBlock) error {
	filenameValue := r.Eval(cs.filenameExpression)
	if filenameValue.IsError() {
		return fmt.Errorf("from import: unable to evaluate filename: %w", filenameValue)
	}

	filename, err := r.Loader.Resolve(filenameValue.String())
	if err != nil {
		return fmt.Errorf("from import: failed to resolve %q: %w", filenameValue.String(), err)
	}

	loader, err := r.Loader.Inherit(filename)
	if err != nil {
		return fmt.Errorf("from import: failed to inherit loader from %q: %w", filename, err)
	}

	tpl, err := exec.NewTemplate(filename, r.Config, loader, r.Environment)
	if err != nil {
		return fmt.Errorf("from import: unable to load template %q: %w", filename, err)
	}

	// Build the child context. "with context" means the imported template
	// sees the current template's variables (facts, settings, etc.).
	var childCtx *exec.Context
	if cs.withContext {
		childCtx = r.Environment.Context.Inherit()
	} else {
		childCtx = exec.EmptyContext()
	}

	childEnv := &exec.Environment{
		Context:           childCtx,
		Tests:             r.Environment.Tests,
		Filters:           r.Environment.Filters,
		ControlStructures: r.Environment.ControlStructures,
		Methods:           r.Environment.Methods,
	}

	// Execute the template to evaluate {% set %} statements, discarding output.
	// Use the parent renderer's loader (not the inherited one) so that paths
	// inside the imported template resolve from the same root — matching Salt
	// behavior where all paths are absolute from the file server root.
	childRenderer := exec.NewRenderer(childEnv, io.Discard, r.Config, r.Loader, tpl)
	if err := childRenderer.Execute(); err != nil {
		return fmt.Errorf("from import: execute %q: %w", filename, err)
	}

	// Extract requested names — first try context variables (from {% set %}),
	// then fall back to macros (for backward compatibility with gonja's default).
	macros := tpl.Macros()
	for alias, name := range cs.as {
		if val, found := childEnv.Context.Get(name); found {
			r.Environment.Context.Set(alias, val)
		} else if node, ok := macros[name]; ok {
			fn, err := exec.MacroNodeToFunc(node, r)
			if err != nil {
				return fmt.Errorf("from import: unable to convert macro %q: %w", name, err)
			}
			r.Environment.Context.Set(alias, fn)
		} else {
			return fmt.Errorf("from import: name %q not found in %q", name, filename)
		}
	}

	return nil
}

func zesterFromParser(p *parser.Parser, args *parser.Parser) (nodes.ControlStructure, error) {
	cs := &fromImportControlStructure{
		location: p.Current(),
		as:       map[string]string{},
	}

	if args.End() {
		return nil, args.Error("from: expected filename expression", nil)
	}

	filename, err := args.ParseExpression()
	if err != nil {
		return nil, fmt.Errorf("from: expected filename expression: %w", err)
	}
	cs.filenameExpression = filename

	if args.MatchName("import") == nil {
		return nil, args.Error("from: expected 'import' keyword", args.Current())
	}

	for !args.End() {
		name := args.Match(tokens.Name)
		if name == nil {
			return nil, args.Error("from: expected name to import", args.Current())
		}

		if args.MatchName("as") != nil {
			alias := args.Match(tokens.Name)
			if alias == nil {
				return nil, args.Error("from: expected alias after 'as'", nil)
			}
			cs.as[alias.Val] = name.Val
		} else {
			cs.as[name.Val] = name.Val
		}

		if tok := args.MatchName("with", "without"); tok != nil {
			if args.MatchName("context") != nil {
				cs.withContext = tok.Val == "with"
				break
			}
			args.Stream().Backup()
		}

		if args.End() {
			break
		}

		if args.Match(tokens.Comma) == nil {
			return nil, args.Error("from: expected ','", nil)
		}
	}

	return cs, nil
}
