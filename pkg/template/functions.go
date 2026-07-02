package template

import (
	"github.com/nikolalohinski/gonja/v2/exec"
)

// registerGlobalFunctions adds Zester-specific global functions to the
// template context. These are available as callable functions in templates,
// e.g. {{ basket("role:webserver", "network.ip_addrs") }}.
func registerGlobalFunctions(ctx *exec.Context, basketFn BasketFunc, moduleFn ModuleFunc) {
	ctx.Set("basket", makeBasketFunction(basketFn))
	ctx.Set("mlist", func(items ...any) *MutableList {
		return NewMutableList(items...)
	})
	// Function-call fallback for the salt accessor, for dynamic module
	// names: {{ salt_call('pkg.version', 'nginx') }}. The subscript form
	// {{ salt['pkg.version']('nginx') }} is handled per-render in
	// buildContext (see module_accessor.go).
	ctx.Set("salt_call", makeSaltCallFunction(moduleFn))
}

// makeBasketFunction creates the basket() global function. It queries
// basket data from other peels based on a target pattern and function name.
//
// Usage in templates:
//
//	{% for peel in basket("role:webserver", "network.ip_addrs") %}
//	  allow {{ peel.value }};
//	{% endfor %}
func makeBasketFunction(fn BasketFunc) func(target, function string) []map[string]any {
	if fn == nil {
		return func(target, function string) []map[string]any {
			return []map[string]any{}
		}
	}
	return fn
}
