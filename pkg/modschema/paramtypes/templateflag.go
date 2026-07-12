package paramtypes

import (
	"fmt"
	"reflect"
	"strings"
)

// TemplateFlag is the semantic value for a "render this through the template
// engine?" parameter (for example file.managed's template). It accepts:
//
//   - a bool,
//   - the string "jinja" (Salt-compatible spelling of "yes, Jinja"), and
//   - any truthy/falsy string (true/yes/1/on, false/no/0/off).
//
// Accepting the truthy strings is BD-2: a CLI `template=true` arrives as the
// string "true", which the legacy `v == "jinja"` check silently ignored. Zester
// has a single template engine (Jinja), so the flag reduces to Enabled().
type TemplateFlag struct {
	declared bool
	enabled  bool
}

// NewTemplateFlag returns a declared TemplateFlag. The zero value is undeclared.
func NewTemplateFlag(enabled bool) TemplateFlag {
	return TemplateFlag{declared: true, enabled: enabled}
}

// Declared reports whether the flag was supplied.
func (t TemplateFlag) Declared() bool { return t.declared }

// Enabled reports whether template rendering is requested.
func (t TemplateFlag) Enabled() bool { return t.enabled }

// templateFlagType is the sealed SemanticType descriptor for TemplateFlag.
type templateFlagType struct{}

func (templateFlagType) Name() string         { return "TemplateFlag" }
func (templateFlagType) GoType() reflect.Type { return reflect.TypeOf(TemplateFlag{}) }
func (templateFlagType) Doc() string {
	return "A template-rendering flag. Accepts a bool, the string \"jinja\", or " +
		"any truthy/falsy string (true/yes/1/on, false/no/0/off). Zester renders " +
		"with Jinja, so any enabling value means \"render with Jinja\"."
}
func (templateFlagType) JSONSchema() map[string]any {
	return map[string]any{
		"oneOf": []any{
			map[string]any{"type": "boolean"},
			map[string]any{"type": "string"},
		},
	}
}
func (templateFlagType) sealed() {}

func (templateFlagType) Decode(in Input) (any, error) {
	switch v := in.Raw.(type) {
	case bool:
		return TemplateFlag{declared: true, enabled: v}, nil
	case string:
		if strings.EqualFold(strings.TrimSpace(v), "jinja") {
			return TemplateFlag{declared: true, enabled: true}, nil
		}
		b, err := parseBoolishString(v)
		if err != nil {
			return TemplateFlag{}, fmt.Errorf("paramtypes: TemplateFlag: %q is not a template flag (bool, \"jinja\", or truthy string)", v)
		}
		return TemplateFlag{declared: true, enabled: b}, nil
	default:
		return TemplateFlag{}, fmt.Errorf("paramtypes: TemplateFlag: cannot interpret %T as a template flag", in.Raw)
	}
}
