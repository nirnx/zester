package peeld

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// moduleDispatchTimeout bounds execution-module calls made from template
// rendering (salt['mod.func'](...)); a hung command must not block settings
// resolution — and with it the exec mutex — indefinitely.
const moduleDispatchTimeout = 60 * time.Second

// moduleDispatch backs the Jinja salt['mod.func'](...) accessor in
// templates. Data-access functions (grains.*/pillar.*) resolve locally
// against facts/settings; other names route to the imperative exec
// registry. Positional args are best-effort mapped (arg[0] -> "name").
func (a *Agent) moduleDispatch(name string, args []any, kwargs map[string]any) (any, error) {
	posStr := func(i int) string {
		if i < len(args) {
			return fmt.Sprintf("%v", args[i])
		}
		return ""
	}
	switch name {
	case "grains.get", "grains.item":
		if v := lookupNestedKey(a.mgr.GetFacts(), posStr(0)); v != nil {
			return v, nil
		}
		if len(args) > 1 {
			return args[1], nil
		}
		return "", nil
	case "grains.items":
		return a.mgr.GetFacts(), nil
	case "pillar.get", "settings.get":
		a.basketScopeMu.RLock()
		cs := a.cachedSettings
		a.basketScopeMu.RUnlock()
		if v := lookupNestedKey(cs, posStr(0)); v != nil {
			return v, nil
		}
		if len(args) > 1 {
			return args[1], nil
		}
		return "", nil
	case "pillar.items", "settings.items":
		a.basketScopeMu.RLock()
		cs := a.cachedSettings
		a.basketScopeMu.RUnlock()
		return cs, nil
	}
	if a.execReg.Has(name) {
		m := make(map[string]any, len(kwargs)+2)
		for k, v := range kwargs {
			m[k] = v
		}
		if len(args) > 0 {
			if _, ok := m["name"]; !ok {
				m["name"] = args[0]
			}
		}
		m["_args"] = args
		// Bounded: a hung template-invoked command must not block
		// settings resolution (and with it the exec mutex) forever.
		callCtx, cancel := context.WithTimeout(context.Background(), moduleDispatchTimeout)
		defer cancel()
		return a.execReg.Call(callCtx, name, a.mctx, m)
	}
	return nil, fmt.Errorf("unknown module %q", name)
}

// lookupNestedKey traverses a nested map using a dot-separated key path.
// For example, "database.port" looks up map["database"].(map[string]any)["port"].
func lookupNestedKey(m map[string]any, key string) any {
	parts := strings.Split(key, ".")
	var current any = m
	for _, part := range parts {
		cm, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = cm[part]
		if !ok {
			return nil
		}
	}
	return current
}
