package compiler

import (
	"github.com/ptorbus/zester/pkg/settings"
	"github.com/ptorbus/zester/pkg/state"
)

// Highstate compiles the highstate for a given peel by resolving the state top file
func (c *Compiler) Highstate(peelID string) (*CompileResult, error) {
	// Load and parse top file (with template rendering)
	top, err := LoadStateTopFile(c.config.StatesDir, c.config.Engine, c.config.Facts, c.config.Settings)
	if err != nil {
		return nil, err
	}

	// Resolve which state refs apply to this peel
	matcher := &settings.SimpleTargetMatcher{}
	refStrings := top.ResolveForPeel(peelID, c.config.Facts, matcher)

	// Convert strings to StateRefs
	var refs []StateRef
	for _, refStr := range refStrings {
		refs = append(refs, StateRef(refStr))
	}

	// If no refs matched, return empty result (not an error)
	if len(refs) == 0 {
		c.config.Logger.Info("compiler: no state refs matched for peel", "peel_id", peelID)
		return &CompileResult{
			States:  []state.State{},
			Sources: []string{},
		}, nil
	}

	// Compile all matched refs
	return c.CompileMultiple(refs)
}
