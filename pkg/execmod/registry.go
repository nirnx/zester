// Package execmod provides Zester's remote-execution module registry.
//
// Remote-execution modules back Salt-style ad-hoc commands such as
// `zester '<target>' pkg.version nginx`. Unlike state modules (pkg/state),
// they are imperative and non-idempotent: each call runs a system query or
// action and returns a plain string result. They delegate all system work to
// the execution providers on an *exec.ModuleContext (Package/Service/Command/
// File) and read host metadata from mctx.Facts.
package execmod

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/nirnx/zester/pkg/exec"
)

// Func is a single remote-execution function. It receives the request context,
// the peel's ModuleContext (providers + facts + settings), and a map of
// caller-supplied arguments, and returns a human-readable string result.
type Func func(ctx context.Context, mctx *exec.ModuleContext, args map[string]any) (string, error)

// ErrUnknownFunction is returned by Call when the named function is not
// registered. Callers can use errors.Is to distinguish "no such function"
// from a function that ran and failed.
var ErrUnknownFunction = fmt.Errorf("execmod: unknown function")

// Registry maps remote-execution function names (e.g. "pkg.version") to their
// implementations. It is safe for concurrent use.
type Registry struct {
	mu    sync.RWMutex
	funcs map[string]Func
}

// NewRegistry returns an empty Registry. Use DefaultRegistry for the built-in
// starter set.
func NewRegistry() *Registry {
	return &Registry{funcs: make(map[string]Func)}
}

// Register adds or replaces the function registered under name. A nil fn is
// ignored so callers cannot accidentally register an un-callable entry.
func (r *Registry) Register(name string, fn Func) {
	if fn == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.funcs[name] = fn
}

// Has reports whether a function is registered under name.
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.funcs[name]
	return ok
}

// Names returns the registered function names in sorted order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.funcs))
	for name := range r.funcs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Call looks up name and invokes it. If name is not registered it returns
// ErrUnknownFunction. A nil args map is normalised to an empty map so
// functions need not nil-check.
func (r *Registry) Call(ctx context.Context, name string, mctx *exec.ModuleContext, args map[string]any) (string, error) {
	r.mu.RLock()
	fn, ok := r.funcs[name]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownFunction, name)
	}
	if args == nil {
		args = map[string]any{}
	}
	return fn(ctx, mctx, args)
}
