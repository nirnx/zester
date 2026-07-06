// Package settings implements the Zester settings system (Salt pillars equivalent).
// It handles loading .zy (YAML + Gonja template) files, rendering them per-peel
// with facts context, merging targeted settings, and encrypting sensitive fields
// using NaCl box encryption with the peel's nkey.
package settings

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/nirnx/zester/pkg/template"
)

// DefaultSettingsDir is the default directory for settings files.
const DefaultSettingsDir = "/srv/zester/settings"

// EncryptedTag is the YAML tag that marks a value for per-peel encryption.
const EncryptedTag = "!encrypted"

// LoadFile reads and parses a .zy file, rendering it through the template
// engine with the given facts and settings context.
func LoadFile(eng *template.Engine, path string, facts, currentSettings map[string]any) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("settings: read %s: %w", path, err)
	}

	rendered, err := eng.RenderString(filepath.Base(path), string(data), template.RenderContext{
		Facts:    facts,
		Settings: currentSettings,
	})
	if err != nil {
		return nil, fmt.Errorf("settings: render %s: %w", path, err)
	}

	var result map[string]any
	if err := yaml.Unmarshal([]byte(rendered), &result); err != nil {
		return nil, fmt.Errorf("settings: parse YAML in %s: %w", path, err)
	}
	return result, nil
}

// MergeSettings deep-merges overlay into base. Overlay values take precedence.
// Maps are merged recursively; other types are replaced.
func MergeSettings(base, overlay map[string]any) map[string]any {
	result := make(map[string]any, len(base))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range overlay {
		if baseMap, ok := result[k].(map[string]any); ok {
			if overlayMap, ok := v.(map[string]any); ok {
				result[k] = MergeSettings(baseMap, overlayMap)
				continue
			}
		}
		result[k] = v
	}
	return result
}
