package compiler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nirnx/zester/pkg/settings"
	"github.com/nirnx/zester/pkg/template"
	"gopkg.in/yaml.v3"
)

// StateTopFile represents a parsed state top file
type StateTopFile struct {
	Environments []StateEnvironment
}

// StateEnvironment represents a single environment in the top file
type StateEnvironment struct {
	Name    string
	Entries []StateTargetEntry
}

// StateTargetEntry represents a target pattern and its state refs
type StateTargetEntry struct {
	Pattern   string
	StateRefs []string
}

// ParseStateTopFile parses a state top file from YAML bytes
func ParseStateTopFile(data []byte) (*StateTopFile, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("compiler: parse state top file: %w", err)
	}

	top := &StateTopFile{}

	// Root should be a mapping node
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return top, nil
	}

	root := doc.Content[0]

	// Iterate over environment entries (key-value pairs)
	for i := 0; i+1 < len(root.Content); i += 2 {
		envName := root.Content[i].Value
		envNode := root.Content[i+1]

		var entries []StateTargetEntry

		// Each environment is a mapping of target patterns to state ref lists
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

				entries = append(entries, StateTargetEntry{
					Pattern:   pattern,
					StateRefs: refs,
				})
			}
		}

		top.Environments = append(top.Environments, StateEnvironment{
			Name:    envName,
			Entries: entries,
		})
	}

	return top, nil
}

// LoadStateTopFile loads and parses the state top file from statesDir
func LoadStateTopFile(statesDir string, engine *template.Engine, facts, settingsData map[string]any) (*StateTopFile, error) {
	path := filepath.Join(statesDir, "top.zy")

	// Read file
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("compiler: read state top file: %w", err)
	}

	// Render as template
	rendered, err := engine.RenderString(path, string(content), template.RenderContext{
		Facts:    facts,
		Settings: settingsData,
	})
	if err != nil {
		return nil, fmt.Errorf("compiler: render state top file: %w", err)
	}

	// Parse YAML
	return ParseStateTopFile([]byte(rendered))
}

// ResolveForPeel resolves which state refs apply to a given peel
func (t *StateTopFile) ResolveForPeel(peelID string, facts map[string]any, matcher settings.TargetMatcher) []string {
	var refs []string
	seen := make(map[string]bool)

	for _, env := range t.Environments {
		for _, entry := range env.Entries {
			if matcher.Match(entry.Pattern, peelID, facts) {
				for _, ref := range entry.StateRefs {
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

// SimpleTargetMatcher is a basic target matcher implementation
// (copied from settings package pattern to avoid circular dependency)
type SimpleTargetMatcher struct{}

// Match checks if a pattern matches a peel ID and facts
func (m *SimpleTargetMatcher) Match(pattern, peelID string, facts map[string]any) bool {
	// Wildcard match
	if pattern == "*" {
		return true
	}

	// Fact-based match (e.g., "role:webserver")
	if parts := strings.SplitN(pattern, ":", 2); len(parts) == 2 {
		key, val := parts[0], parts[1]
		if factVal, ok := facts[key]; ok {
			return fmt.Sprintf("%v", factVal) == val
		}
		return false
	}

	// Glob pattern match
	matched, err := filepath.Match(pattern, peelID)
	if err != nil {
		return false
	}
	return matched
}
