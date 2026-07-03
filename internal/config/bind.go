package config

// bind.go provides a reflection-based flag binder that collapses the
// per-daemon triplicate of flag definitions, YAML struct fields, and
// hand-written flag.Visit switches into a single declarative source: the
// config struct itself.
//
// Struct fields opt in via tags:
//
//	type MasterDaemonConfig struct {
//	    NatsURL string      `yaml:"nats_url" flag:"nats-url" usage:"NATS server URL"`
//	    GitFS   GitFSConfig `yaml:"gitfs"`   // nested: recursed into
//	}
//
// Intended call sequence (matches the daemons' existing precedence of
// flag > config file > struct default):
//
//	cfg := MasterDaemonDefaults()        // 1. defaults pre-populated in struct
//	BindFlags(fs, &cfg)                  // 2. flags registered, defaults from struct
//	fs.Parse(os.Args[1:])                // 3. parse (values held by the FlagSet)
//	cfg, _ = LoadMasterDaemon(path)      // 4. YAML overwrites struct
//	ApplyVisited(fs, &cfg)               // 5. explicitly-set flags win over YAML
//
// Because ApplyVisited only touches flags the user actually passed
// (flag.FlagSet.Visit), an explicitly empty value such as
// --gitfs-remotes "" still overrides a non-empty YAML value.

import (
	"flag"
	"fmt"
	"reflect"
	"strings"
	"time"
)

var (
	durationType       = reflect.TypeOf(time.Duration(0))
	configDurationType = reflect.TypeOf(Duration(0))
	stringSliceType    = reflect.TypeOf([]string(nil))
)

// isDurationType reports whether t is one of the duration flag types:
// time.Duration or the YAML-tolerant config.Duration (both bind as
// flag.Duration; the flag form is identical).
func isDurationType(t reflect.Type) bool {
	return t == durationType || t == configDurationType
}

// BindFlags registers one flag on fs per `flag:"..."`-tagged field of the
// struct pointed to by cfg, using the field's current value as the flag
// default (callers set defaults by pre-populating the struct). The optional
// `usage:"..."` tag supplies the help text.
//
// Supported field types: string, bool, int, int64, time.Duration (and the
// YAML-tolerant config.Duration), float64, and []string (flag value parsed
// as a comma-separated list, empty items dropped). Untagged struct fields
// are recursed into; other untagged fields are skipped. A tag of `flag:"-"`
// skips the field explicitly.
//
// Duplicate flag names (within cfg or against flags already registered on
// fs), unsupported tagged field types, and non-struct-pointer cfg values
// return descriptive errors.
func BindFlags(fs *flag.FlagSet, cfg any) error {
	fields, err := collectFlagFields(cfg)
	if err != nil {
		return err
	}
	for _, bf := range fields {
		if fs.Lookup(bf.name) != nil {
			return fmt.Errorf("config: flag %q (field %s) already registered on flag set", bf.name, bf.path)
		}
		v := bf.value
		switch {
		case isDurationType(v.Type()):
			fs.Duration(bf.name, time.Duration(v.Int()), bf.usage)
		case isStringSlice(v.Type()):
			def := make([]string, v.Len())
			for i := range def {
				def[i] = v.Index(i).String()
			}
			fs.Var(&stringSliceValue{items: def}, bf.name, bf.usage)
		default:
			switch v.Kind() {
			case reflect.String:
				fs.String(bf.name, v.String(), bf.usage)
			case reflect.Bool:
				fs.Bool(bf.name, v.Bool(), bf.usage)
			case reflect.Int:
				fs.Int(bf.name, int(v.Int()), bf.usage)
			case reflect.Int64:
				fs.Int64(bf.name, v.Int(), bf.usage)
			case reflect.Float64:
				fs.Float64(bf.name, v.Float(), bf.usage)
			}
		}
	}
	return nil
}

// ApplyVisited writes the value of every flag the user explicitly set
// (per fs.Visit) back into the matching tagged field of the struct pointed
// to by cfg. Call it after fs.Parse AND after loading the YAML config file
// into cfg: it reproduces the daemons' precedence of flag > config file >
// default, including the explicit-empty-string case (e.g. --gitfs-remotes ""
// clearing a YAML-provided list).
//
// Visited flags with no matching tagged field (e.g. --config itself) are
// ignored, so extra flags may be registered on fs alongside BindFlags.
func ApplyVisited(fs *flag.FlagSet, cfg any) error {
	fields, err := collectFlagFields(cfg)
	if err != nil {
		return err
	}
	byName := make(map[string]flagField, len(fields))
	for _, bf := range fields {
		byName[bf.name] = bf
	}

	var firstErr error
	fs.Visit(func(f *flag.Flag) {
		bf, ok := byName[f.Name]
		if !ok {
			return // flag not bound to this struct
		}
		getter, ok := f.Value.(flag.Getter)
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("config: flag %q value type %T does not implement flag.Getter", f.Name, f.Value)
			}
			return
		}
		if err := setFieldValue(bf.value, getter.Get()); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("config: apply flag -%s to field %s: %w", f.Name, bf.path, err)
		}
	})
	return firstErr
}

// flagField is one `flag:"..."`-tagged struct field discovered by
// collectFlagFields.
type flagField struct {
	name  string        // flag name from the tag
	usage string        // help text from the usage tag
	path  string        // dotted Go field path, for error messages
	value reflect.Value // addressable field value
}

// collectFlagFields validates cfg and walks its struct fields (recursing
// into untagged nested structs), returning the tagged fields in declaration
// order. It rejects duplicate flag names and unsupported tagged field types
// so BindFlags and ApplyVisited report identical structural errors.
func collectFlagFields(cfg any) ([]flagField, error) {
	rv := reflect.ValueOf(cfg)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return nil, fmt.Errorf("config: cfg must be a non-nil pointer to a struct, got %T", cfg)
	}
	elem := rv.Elem()
	if elem.Kind() != reflect.Struct {
		return nil, fmt.Errorf("config: cfg must point to a struct, got pointer to %s", elem.Kind())
	}

	var out []flagField
	seen := make(map[string]string) // flag name -> field path
	if err := walkFlagFields(elem, "", &out, seen); err != nil {
		return nil, err
	}
	return out, nil
}

func walkFlagFields(v reflect.Value, prefix string, out *[]flagField, seen map[string]string) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		path := sf.Name
		if prefix != "" {
			path = prefix + "." + sf.Name
		}
		name := sf.Tag.Get("flag")
		if name == "-" {
			continue
		}
		fv := v.Field(i)
		if name == "" {
			// Untagged: recurse into plain nested structs, skip everything else.
			if fv.Kind() == reflect.Struct {
				if err := walkFlagFields(fv, path, out, seen); err != nil {
					return err
				}
			}
			continue
		}
		if !supportedFlagType(sf.Type) {
			return fmt.Errorf("config: field %s has unsupported flag type %s (supported: string, bool, int, int64, float64, time.Duration, config.Duration, []string)", path, sf.Type)
		}
		if prev, dup := seen[name]; dup {
			return fmt.Errorf("config: duplicate flag name %q (fields %s and %s)", name, prev, path)
		}
		seen[name] = path
		*out = append(*out, flagField{
			name:  name,
			usage: sf.Tag.Get("usage"),
			path:  path,
			value: fv,
		})
	}
	return nil
}

func supportedFlagType(t reflect.Type) bool {
	if isDurationType(t) || isStringSlice(t) {
		return true
	}
	switch t.Kind() {
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int64, reflect.Float64:
		return true
	default:
		return false
	}
}

func isStringSlice(t reflect.Type) bool {
	return t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.String
}

// setFieldValue assigns a parsed flag value (from flag.Getter.Get) to a
// struct field via reflection.
func setFieldValue(field reflect.Value, val any) error {
	switch v := val.(type) {
	case string:
		if field.Kind() == reflect.String {
			field.SetString(v)
			return nil
		}
	case bool:
		if field.Kind() == reflect.Bool {
			field.SetBool(v)
			return nil
		}
	case int:
		if field.Kind() == reflect.Int {
			field.SetInt(int64(v))
			return nil
		}
	case int64:
		if field.Kind() == reflect.Int64 {
			field.SetInt(v)
			return nil
		}
	case time.Duration:
		if isDurationType(field.Type()) {
			field.SetInt(int64(v))
			return nil
		}
	case float64:
		if field.Kind() == reflect.Float64 {
			field.SetFloat(v)
			return nil
		}
	case []string:
		if isStringSlice(field.Type()) {
			field.Set(reflect.ValueOf(v).Convert(field.Type()))
			return nil
		}
	}
	return fmt.Errorf("cannot assign flag value of type %T to field of type %s", val, field.Type())
}

// stringSliceValue is a flag.Value/flag.Getter for []string fields. The flag
// value is parsed as a comma-separated list with whitespace-trimmed items;
// empty items are dropped, so an explicitly empty argument ("") yields a nil
// slice — this is what lets --gitfs-remotes "" disable GitFS even when the
// config file lists remotes.
type stringSliceValue struct {
	items []string
}

func (s *stringSliceValue) String() string {
	if s == nil {
		return ""
	}
	return strings.Join(s.items, ",")
}

func (s *stringSliceValue) Set(v string) error {
	var items []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			items = append(items, p)
		}
	}
	s.items = items
	return nil
}

func (s *stringSliceValue) Get() any {
	return s.items
}
