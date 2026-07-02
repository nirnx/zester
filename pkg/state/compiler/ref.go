package compiler

import (
	"path/filepath"
	"strings"
)

// StateRef is a dot-notation reference to a state file.
//
//	"webserver"        → webserver/init.zy
//	"webserver.config" → webserver/config.zy
type StateRef string

// ResolveToPath converts a dot-notation state reference to a filesystem path.
// Single segment refs resolve to <segment>/init.zy, multi-segment refs to <path>.zy
func (r StateRef) ResolveToPath(statesDir string) string {
	parts := strings.Split(string(r), ".")
	if len(parts) == 1 {
		// Single segment: "webserver" → webserver/init.zy
		return filepath.Join(statesDir, parts[0], "init.zy")
	}
	// Multi-segment: "webserver.config" → webserver/config.zy
	path := filepath.Join(parts...)
	return filepath.Join(statesDir, path+".zy")
}
