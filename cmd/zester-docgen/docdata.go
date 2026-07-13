package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/nirnx/zester/pkg/modschema"
)

// renderDocdataJSON renders pkg/moduledoc/docdata.json (§7 offline path): a
// deterministic JSON array of every spec-carrying module's ModuleInfo, sorted
// by Module name — "gated on spec presence" (currently just pkg.removed).
// pkg/moduledoc embeds this file and serves it through the exact same
// modschema.RenderText a live Spec.Info() would render (TestDocdataMatchesLive
// pins the two identical).
func renderDocdataJSON(infos []modschema.ModuleInfo) ([]byte, error) {
	// Non-nil from the start (F3): a nil []modschema.ModuleInfo encodes as the
	// JSON literal `null`, not `[]` — with zero spec-carrying modules (a state
	// no migration has reached yet), infos filters down to nothing and the
	// `var specd []modschema.ModuleInfo` zero value stayed nil all the way to
	// json.Marshal. pkg/moduledoc's embed.go decodes docdata.json straight into
	// a `[]modschema.ModuleInfo` — `null` unmarshals fine there today, but the
	// artifact must still commit to the one JSON shape it documents (an array)
	// regardless of how many modules currently populate it.
	specd := []modschema.ModuleInfo{}
	for _, mi := range infos {
		// Spec-carrying modules AND the dispatch specials: the specials have a
		// Doc but no param schema (HasSpec false), yet offline `zester doc`
		// must answer them exactly as live sys.doc does (review fix).
		if mi.HasSpec || mi.Kind == modschema.KindDispatch {
			specd = append(specd, mi)
		}
	}
	sort.Slice(specd, func(i, j int) bool { return specd[i].Module < specd[j].Module })

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(specd); err != nil {
		return nil, fmt.Errorf("docgen: render docdata.json: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
