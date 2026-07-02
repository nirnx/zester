package template

import (
	"fmt"
	"regexp"

	"github.com/nikolalohinski/gonja/v2/exec"
)

// ModuleFunc dispatches an execution-module call made from a template via the
// salt accessor, e.g. {{ salt['pkg.version']('nginx') }}. name is the dotted
// "module.function" reference, args are the positional arguments and kwargs
// the keyword arguments of the template call. The returned value is inserted
// into the rendered output (or bound with {% set %}).
type ModuleFunc func(name string, args []any, kwargs map[string]any) (any, error)

// saltSubscriptRe matches literal salt['mod.func'] / salt["mod.func"]
// subscripts in template source.
//
// Gonja has no hook for dynamic subscript resolution (Value.GetItem only
// resolves keys that exist in a Go map), so the engine pre-scans the template
// source for literal salt[...] subscripts and populates the per-render "salt"
// map with a callable proxy for each referenced module name. This covers the
// canonical Salt template form. For dynamic module names (e.g. salt[var]) or
// salt[...] uses inside {% include %}d files, use the salt_call() fallback:
// salt_call('mod.func', args..., kw=...).
var saltSubscriptRe = regexp.MustCompile(`\bsalt\s*\[\s*(?:'([^']+)'|"([^"]+)")\s*\]`)

// scanSaltModuleNames extracts the deduplicated list of module names
// referenced by literal salt[...] subscripts in the template source.
func scanSaltModuleNames(source string) []string {
	matches := saltSubscriptRe.FindAllStringSubmatch(source, -1)
	seen := make(map[string]struct{}, len(matches))
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		name := m[1]
		if name == "" {
			name = m[2]
		}
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

// buildSaltAccessor builds the per-render "salt" context object: a map from
// module name to a callable proxy, keyed by the literal salt[...] subscripts
// found in the template source. Subscript-then-call works because gonja
// resolves salt['pkg.version'] via map lookup and then invokes the returned
// Go function: {{ salt['pkg.version']('nginx') }}.
func buildSaltAccessor(source string, fn ModuleFunc) map[string]any {
	names := scanSaltModuleNames(source)
	salt := make(map[string]any, len(names))
	for _, name := range names {
		salt[name] = makeSaltModuleProxy(name, fn)
	}
	return salt
}

// makeSaltModuleProxy returns the callable bound to salt['<name>']. It
// accepts *exec.VarArgs so both positional and keyword arguments are
// forwarded to the dispatcher.
func makeSaltModuleProxy(name string, fn ModuleFunc) func(*exec.VarArgs) (any, error) {
	return func(va *exec.VarArgs) (any, error) {
		return dispatchModule(fn, name, va)
	}
}

// makeSaltCallFunction returns the salt_call(...) global function, the
// function-call fallback form of the salt accessor for dynamic module names:
// salt_call('pkg.version', 'nginx', refresh=true).
func makeSaltCallFunction(fn ModuleFunc) func(*exec.VarArgs) (any, error) {
	return func(va *exec.VarArgs) (any, error) {
		if len(va.Args) < 1 || !va.Args[0].IsString() {
			return nil, fmt.Errorf("template: salt_call: first argument must be a module name string, e.g. salt_call('pkg.version', 'nginx')")
		}
		name := va.Args[0].String()
		rest := &exec.VarArgs{Args: va.Args[1:], KwArgs: va.KwArgs}
		return dispatchModule(fn, name, rest)
	}
}

// dispatchModule converts gonja values to plain Go values and invokes the
// configured ModuleFunc dispatcher.
func dispatchModule(fn ModuleFunc, name string, va *exec.VarArgs) (any, error) {
	if fn == nil {
		return nil, fmt.Errorf("template: salt[%q]: no module dispatcher configured (EngineConfig.ModuleFn is nil); execution-module calls are not available in this render context", name)
	}
	args := make([]any, 0, len(va.Args))
	for _, a := range va.Args {
		v := a.ToGoSimpleType(false)
		if err, ok := v.(error); ok {
			return nil, fmt.Errorf("template: salt[%q]: convert argument: %w", name, err)
		}
		args = append(args, v)
	}
	kwargs := make(map[string]any, len(va.KwArgs))
	for k, kv := range va.KwArgs {
		v := kv.ToGoSimpleType(false)
		if err, ok := v.(error); ok {
			return nil, fmt.Errorf("template: salt[%q]: convert keyword argument %q: %w", name, k, err)
		}
		kwargs[k] = v
	}
	result, err := fn(name, args, kwargs)
	if err != nil {
		return nil, fmt.Errorf("template: salt[%q]: %w", name, err)
	}
	return result, nil
}
