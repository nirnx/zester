package collectors

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultCustomFactsPath is the default location for persistent custom facts.
// Operators create or edit this YAML file directly on the peel; changes are
// picked up automatically on the next collection interval.
const DefaultCustomFactsPath = "/etc/zester/facts"

// Custom reads persistent custom facts from a YAML file on disk.
// This is the Zester equivalent of Salt's /etc/salt/grains.
// Custom facts are merged at the top level of the facts map (not
// nested under a namespace), so "role: webserver" in /etc/zester/facts
// becomes facts["role"] directly -- just like Salt grains.
//
// Example /etc/zester/facts:
//
//	roles:
//	  - webserver
//	  - proxy
//	datacenter: us-east-1
//	tier: production
//
// Usage:
//
//	zester 'web-01' facts.get roles
//	zester 'web-01' facts.get datacenter
//	zester 'web-01' facts.set datacenter us-west-2
type Custom struct {
	// Path to the custom facts YAML file.
	// Defaults to /etc/zester/facts if empty.
	Path string
}

func (c Custom) Name() string            { return "custom" }
func (c Custom) Interval() time.Duration { return 30 * time.Second }

// MergeAtRoot implements facts.RootMerger. Custom facts are merged at the
// top level of the facts map rather than nested under "custom".
func (c Custom) MergeAtRoot() bool { return true }

func (c Custom) Collect(_ context.Context) (map[string]any, error) {
	path := c.Path
	if path == "" {
		path = DefaultCustomFactsPath
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read custom facts %s: %w", path, err)
	}

	if len(data) == 0 {
		return map[string]any{}, nil
	}

	var facts map[string]any
	if err := yaml.Unmarshal(data, &facts); err != nil {
		return nil, fmt.Errorf("parse custom facts %s: %w", path, err)
	}

	if facts == nil {
		return map[string]any{}, nil
	}

	return facts, nil
}
