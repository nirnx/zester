package settings

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// TopFile represents a parsed top.zy file that maps targeting patterns
// to lists of settings file references.
//
// Format:
//
//	base:
//	  '*':
//	    - common.base
//	  'role:webserver':
//	    - webservers.nginx
//	    - webservers.certs
type TopFile struct {
	// Environments is an ordered list of environments preserving YAML document order.
	Environments []Environment
}

// Environment pairs an environment name with its ordered target entries.
type Environment struct {
	Name    string
	Entries []TargetEntry
}

// TargetEntry associates a targeting pattern with a list of settings file references.
type TargetEntry struct {
	// Pattern is the targeting expression (e.g. "*", "role:webserver").
	Pattern string

	// SettingsRefs is the list of settings file references (e.g. "common.base").
	SettingsRefs []string
}

// ParseTopFile parses the raw YAML content of a top.zy file.
// It uses yaml.Node parsing to preserve document order of environments
// and target entries, ensuring deterministic settings precedence.
func ParseTopFile(data []byte) (*TopFile, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("settings: parse top.zy: %w", err)
	}

	top := &TopFile{}

	// doc.Content[0] is the root mapping node.
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return top, nil
	}

	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		envName := root.Content[i].Value
		envNode := root.Content[i+1]

		var entries []TargetEntry
		if envNode.Kind == yaml.MappingNode {
			for j := 0; j+1 < len(envNode.Content); j += 2 {
				pattern := envNode.Content[j].Value
				refsNode := envNode.Content[j+1]
				var refs []string
				if refsNode.Kind == yaml.SequenceNode {
					for _, item := range refsNode.Content {
						refs = append(refs, item.Value)
					}
				}
				entries = append(entries, TargetEntry{Pattern: pattern, SettingsRefs: refs})
			}
		}
		top.Environments = append(top.Environments, Environment{Name: envName, Entries: entries})
	}
	return top, nil
}

// ResolveForPeel returns the ordered list of settings file references
// that apply to a given peel based on its ID and facts.
// The matcher function determines whether a pattern matches the peel.
func (t *TopFile) ResolveForPeel(peelID string, facts map[string]any, matcher TargetMatcher) []string {
	var refs []string
	seen := make(map[string]bool)

	for _, env := range t.Environments {
		for _, entry := range env.Entries {
			if matcher.Match(entry.Pattern, peelID, facts) {
				for _, ref := range entry.SettingsRefs {
					if !seen[ref] {
						seen[ref] = true
						refs = append(refs, ref)
					}
				}
			}
		}
	}
	return refs
}

// TargetMatcher evaluates whether a peel matches a targeting pattern.
type TargetMatcher interface {
	Match(pattern, peelID string, facts map[string]any) bool
}

// SimpleTargetMatcher provides basic glob and fact-based matching.
type SimpleTargetMatcher struct{}

// Match implements TargetMatcher with support for:
//   - "*" - matches all peels
//   - Glob patterns - "web*" matches "web-01"
//   - Fact matchers - "key:value" matches if facts[key] == value
func (m *SimpleTargetMatcher) Match(pattern, peelID string, facts map[string]any) bool {
	if pattern == "*" {
		return true
	}

	// Fact-based targeting: "key:value"
	if parts := strings.SplitN(pattern, ":", 2); len(parts) == 2 {
		key, val := parts[0], parts[1]
		if factVal, ok := facts[key]; ok {
			return fmt.Sprintf("%v", factVal) == val
		}
		return false
	}

	// Simple glob matching.
	matched, err := filepath.Match(pattern, peelID)
	if err != nil {
		return false
	}
	return matched
}
