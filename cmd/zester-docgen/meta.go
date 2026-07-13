package main

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// modulesMeta is the Fumadocs meta.json shape for the modules guide directory.
// "index" is deliberately never listed (§8) — Fumadocs includes it implicitly.
type modulesMeta struct {
	Title string   `json:"title"`
	Pages []string `json:"pages"`
}

// renderMetaJSON renders the wholesale modules meta.json from families:
// separators as literal "---<Label>---" page entries, "index" never listed,
// 2-space indent, no trailing newline — byte-exact to the live file's format.
func renderMetaJSON() ([]byte, error) {
	m := modulesMeta{Title: "Modules"}
	for _, fam := range families {
		m.Pages = append(m.Pages, fmt.Sprintf("---%s---", fam.Label))
		m.Pages = append(m.Pages, fam.Pages...)
	}
	// A plain json.Marshal/MarshalIndent HTML-escapes "&" (and "<"/">") by
	// default, which would turn "User & Group" into "User & Group" — an
	// Encoder with SetEscapeHTML(false) is required for a byte-exact,
	// human-authored-looking separator label. Encoder.Encode also appends a
	// trailing newline the live file doesn't have, so it's trimmed.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, fmt.Errorf("docgen: render meta.json: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
