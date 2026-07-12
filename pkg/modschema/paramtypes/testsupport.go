package paramtypes

import (
	"reflect"
	"testing"
)

// This file is TEST INFRASTRUCTURE that, unavoidably, lives in the production
// package. The SemanticType interface is sealed (unexported sealed method), so a
// test in any OTHER package — pkg/modschema in particular — cannot construct a
// SemanticType to exercise the framework's semantic dispatch. RegisterTestType
// is the single seam that lets such tests obtain a registered, paramtypes-owned
// semantic type. It does NOT weaken the seal: the returned value is a
// paramtypes-owned wrapper, so no foreign type can be smuggled in, and it panics
// once the registry is sealed (production seals at startup), so it can never add
// a type to a live fleet. It carries no vocabulary semantics.

// dynamicSemanticType is a paramtypes-owned SemanticType assembled from parts.
type dynamicSemanticType struct {
	name   string
	goType reflect.Type
	decode func(Input) (any, error)
	doc    string
	schema map[string]any
}

func (d *dynamicSemanticType) Name() string         { return d.name }
func (d *dynamicSemanticType) GoType() reflect.Type { return d.goType }
func (d *dynamicSemanticType) Decode(in Input) (any, error) {
	if d.decode == nil {
		return reflect.Zero(d.goType).Interface(), nil
	}
	return d.decode(in)
}
func (d *dynamicSemanticType) Doc() string                { return d.doc }
func (d *dynamicSemanticType) JSONSchema() map[string]any { return d.schema }
func (d *dynamicSemanticType) sealed()                    {}

// RegisterTestType builds and registers a paramtypes-owned SemanticType from the
// supplied parts and returns a function that unregisters it. Use it from
// cross-package tests of the decoding framework; production code must not call
// it (it panics once the registry is sealed). goType is the field Go type this
// semantic type dispatches for and the concrete type decode must return.
func RegisterTestType(name string, goType reflect.Type, decode func(Input) (any, error), doc string, schema map[string]any) (remove func()) {
	// Guard: this seam exists only for framework tests. Outside `go test`
	// (testing.Testing() == false) it panics, so it is inert in production
	// binaries even before Seal() is called (spec §3 test-hook clause).
	if !testing.Testing() {
		panic("paramtypes: RegisterTestType called outside go test")
	}
	st := &dynamicSemanticType{name: name, goType: goType, decode: decode, doc: doc, schema: schema}
	register(st)
	return func() { unregister(st) }
}
