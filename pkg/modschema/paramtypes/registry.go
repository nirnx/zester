package paramtypes

import (
	"fmt"
	"reflect"
	"sort"
	"sync"
)

// The registry is the single home of the sealed semantic-type vocabulary.
// Concrete types register themselves from the package init (vocabulary.go);
// the registry keys them by both stable name and Go type so the framework can
// dispatch a struct field's Go type to its decoder via ForGoType. Registration
// is unexported: only paramtypes-owned types ever enter the registry (RegisterTestType
// wraps a paramtypes-owned type for cross-package tests — see testsupport.go).
var (
	registryMu sync.RWMutex
	byName     = map[string]SemanticType{}
	byGoType   = map[reflect.Type]SemanticType{}
	sealedFlag bool
)

// register adds a semantic type to the registry. It panics on a nil type, an
// empty name, a nil GoType, a duplicate name or Go type, or registration after
// Seal — every one is a programming error caught at startup, never a runtime
// condition.
func register(st SemanticType) {
	registryMu.Lock()
	defer registryMu.Unlock()

	if sealedFlag {
		panic("paramtypes: register after Seal")
	}
	if st == nil {
		panic("paramtypes: register(nil)")
	}
	name := st.Name()
	if name == "" {
		panic("paramtypes: register: empty Name()")
	}
	gt := st.GoType()
	if gt == nil {
		panic(fmt.Sprintf("paramtypes: register %q: nil GoType()", name))
	}
	if _, dup := byName[name]; dup {
		panic(fmt.Sprintf("paramtypes: register: duplicate name %q", name))
	}
	if _, dup := byGoType[gt]; dup {
		panic(fmt.Sprintf("paramtypes: register: duplicate GoType %s (name %q)", gt, name))
	}
	byName[name] = st
	byGoType[gt] = st
}

// unregister removes st from the registry if it is still the registered entry
// for its name and Go type. It exists for test cleanup (RegisterTestType).
func unregister(st SemanticType) {
	registryMu.Lock()
	defer registryMu.Unlock()

	if cur, ok := byName[st.Name()]; ok && cur == st {
		delete(byName, st.Name())
	}
	if cur, ok := byGoType[st.GoType()]; ok && cur == st {
		delete(byGoType, st.GoType())
	}
}

// Lookup returns the semantic type registered under name.
func Lookup(name string) (SemanticType, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	st, ok := byName[name]
	return st, ok
}

// ForGoType returns the semantic type whose GoType is t. This is the framework's
// dispatch key: modschema resolves a struct field's Go type to its semantic
// decoder here. A non-primitive field type with no registered semantic type is a
// modschema.Compile error (the seal wall for unregistered types).
func ForGoType(t reflect.Type) (SemanticType, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	st, ok := byGoType[t]
	return st, ok
}

// All returns every registered semantic type, sorted by Name.
func All() []SemanticType {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]SemanticType, 0, len(byName))
	for _, st := range byName {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Seal freezes the registry: no further registration is possible and any attempt
// panics. Production startup seals after the vocabulary is registered so the
// sealed set is exactly the compiled-in vocabulary — closing the only avenue by
// which RegisterTestType could inject a type at runtime.
func Seal() {
	registryMu.Lock()
	sealedFlag = true
	registryMu.Unlock()
}

// Sealed reports whether the registry has been sealed.
func Sealed() bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return sealedFlag
}
