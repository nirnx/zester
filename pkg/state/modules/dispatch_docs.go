package modules

import (
	"sort"
	"strings"

	"github.com/nirnx/zester/pkg/modschema"
)

// DispatchSpecials is the single source of truth for the peel's dispatch
// surfaces — the module names that the peel handles itself rather than routing
// to a state-module build or an execmod function (keystone spec §7). It drives
// BOTH peel dispatch sites: execModule's dispatch (LookupDispatch → a bound
// specialHandler, replacing the legacy if/else chain) and readOnlyModule's
// classification (IsReadOnlyDispatch derives the read-only prefix set). A
// TestDispatchTableBound pin keeps the two sites and their handlers 1:1.
//
// Each row carries a full modschema.Doc (Kind = dispatch — Effects.Execution is
// required, no Check/Apply/Revert). The prose is drift-corrected against the
// ACTUAL handler code in internal/peeld/exec.go (execFactsModule,
// execSettingsModule, execEventSend, dispatchStateModule).
//
// Structural precedence (LookupDispatch): exact match → longest matching prefix
// → table order as the final deterministic tiebreak. The prefix-family
// catch-alls (facts./settings./pillar.) are listed AFTER every concrete entry
// so an exact concrete name (e.g. facts.set) always wins over its family; an
// unrecognized subfunction (facts.xyz) falls through to the family catch-all,
// which routes it to the in-handler "unknown <family> function" error.

// DispatchMatch classifies how a DispatchSpecial's Name is matched against a
// requested module name. It is the spec's "Kind (exact | prefix-family
// catch-all)" — distinct from modschema.Kind (which is always KindDispatch for
// these surfaces).
type DispatchMatch uint8

const (
	// DispatchExact matches when Name equals the requested module name verbatim
	// (a concrete special such as "facts.get" or "state.apply").
	DispatchExact DispatchMatch = iota
	// DispatchPrefix matches when the requested module name begins with Name (a
	// "<family>." catch-all such as "facts."). Only consulted when no exact
	// entry matched.
	DispatchPrefix
)

// DispatchSpecial is one peel dispatch surface: its addressable name, how that
// name matches, whether it runs on the read-only fast path (outside execMu),
// and its documentation metadata.
type DispatchSpecial struct {
	// Name is the concrete module name (DispatchExact) or the "<family>."
	// prefix (DispatchPrefix).
	Name string
	// Match is how Name is matched (exact | prefix-family catch-all).
	Match DispatchMatch
	// ReadOnly reports whether the surface runs on the peel's concurrent
	// read-only path (never mutating the managed system or the shared
	// ModuleContext). It backs IsReadOnlyDispatch and, through it,
	// readOnlyModule.
	ReadOnly bool
	// Doc is the surface's documentation metadata (Kind dispatch —
	// Effects.Execution required).
	Doc modschema.Doc
}

// DispatchSpecials is the ordered table. Concrete (DispatchExact) entries come
// first; the prefix-family (DispatchPrefix) catch-alls follow, so an exact name
// always outranks its family.
var DispatchSpecials = []DispatchSpecial{
	{Name: "state.apply", Match: DispatchExact, ReadOnly: false, Doc: docStateApply},
	{Name: "state.highstate", Match: DispatchExact, ReadOnly: false, Doc: docStateHighstate},

	{Name: "facts.get", Match: DispatchExact, ReadOnly: true, Doc: docFactsGet},
	{Name: "facts.items", Match: DispatchExact, ReadOnly: true, Doc: docFactsItems},
	{Name: "facts.keys", Match: DispatchExact, ReadOnly: true, Doc: docFactsKeys},
	{Name: "facts.set", Match: DispatchExact, ReadOnly: false, Doc: docFactsSet},

	{Name: "settings.get", Match: DispatchExact, ReadOnly: true, Doc: docSettingsGet},
	{Name: "settings.items", Match: DispatchExact, ReadOnly: true, Doc: docSettingsItems},
	{Name: "settings.keys", Match: DispatchExact, ReadOnly: true, Doc: docSettingsKeys},

	// pillar.* are the Salt-compat aliases of settings.* (pillar maps to
	// settings), handled by the same in-handler code.
	{Name: "pillar.get", Match: DispatchExact, ReadOnly: true, Doc: docPillarGet},
	{Name: "pillar.items", Match: DispatchExact, ReadOnly: true, Doc: docPillarItems},
	{Name: "pillar.keys", Match: DispatchExact, ReadOnly: true, Doc: docPillarKeys},

	{Name: "event.send", Match: DispatchExact, ReadOnly: false, Doc: docEventSend},

	// Prefix-family catch-alls — AFTER every concrete entry (exact wins).
	{Name: "facts.", Match: DispatchPrefix, ReadOnly: true, Doc: docFactsFamily},
	{Name: "settings.", Match: DispatchPrefix, ReadOnly: true, Doc: docSettingsFamily},
	{Name: "pillar.", Match: DispatchPrefix, ReadOnly: true, Doc: docPillarFamily},
}

// LookupDispatch resolves module against the dispatch table using the spec's
// structural precedence: an exact concrete entry wins immediately; otherwise
// the longest matching prefix-family entry wins, with earlier table order
// breaking a length tie. It reports (zero, false) when module is not a dispatch
// surface (the caller then falls through to execmod/state-registry dispatch).
func LookupDispatch(module string) (DispatchSpecial, bool) {
	var best DispatchSpecial
	bestLen := -1
	found := false
	for _, sp := range DispatchSpecials {
		switch sp.Match {
		case DispatchExact:
			if sp.Name == module {
				// Exact match is unconditionally highest precedence.
				return sp, true
			}
		case DispatchPrefix:
			if strings.HasPrefix(module, sp.Name) && len(sp.Name) > bestLen {
				best = sp
				bestLen = len(sp.Name)
				found = true
			}
		}
	}
	return best, found
}

// IsReadOnlyDispatch reports whether module is a dispatch surface that runs on
// the read-only fast path. It is false for a non-dispatch module and for the
// mutating dispatch surfaces (facts.set, event.send, state.apply/highstate). It
// is the table-derived core of the peel's readOnlyModule classifier.
func IsReadOnlyDispatch(module string) bool {
	sp, ok := LookupDispatch(module)
	return ok && sp.ReadOnly
}

// DispatchNames returns the sorted addressable (DispatchExact) special names —
// the callable dispatch surfaces. The prefix-family catch-alls are routing
// entries, not callable names, so they are excluded. Used to merge dispatch
// surfaces into the unified sys.doc / sys.list_functions index.
func DispatchNames() []string {
	out := make([]string, 0, len(DispatchSpecials))
	for _, sp := range DispatchSpecials {
		if sp.Match == DispatchExact {
			out = append(out, sp.Name)
		}
	}
	sort.Strings(out)
	return out
}

// DispatchInfo returns the documentation view of a concrete dispatch surface,
// mirroring dispatch precedence for sys.doc's per-name lookup. Only DispatchExact
// entries are addressable documentation targets; a prefix-family catch-all match
// (an unrecognized facts.xyz) reports (zero, false) so sys.doc says "no
// documentation" rather than surfacing the family router.
func DispatchInfo(name string) (modschema.ModuleInfo, bool) {
	sp, ok := LookupDispatch(name)
	if !ok || sp.Match != DispatchExact {
		return modschema.ModuleInfo{}, false
	}
	return modschema.ModuleInfo{
		Module: sp.Name,
		Kind:   modschema.KindDispatch,
		// Cloned on egress: the table is process-wide shared state and every
		// returned documentation view must be safe to vandalize (round 5).
		Doc:     sp.Doc.Clone(),
		HasSpec: false,
	}, true
}

// --- Docs (drift-corrected from internal/peeld/exec.go) ----------------------

var docStateApply = modschema.Doc{
	Summary: "Compile and apply a named state set on the peel.",
	Description: "`state.apply` compiles the referenced state set and applies it. The state " +
		"reference is read from `state` (canonical), `mods` (Salt's kwarg name), or the " +
		"request's state ID, in that order. It is the ad-hoc single-tree counterpart of " +
		"`state.highstate`. Called WITHOUT any state reference it is an alias for " +
		"`state.highstate` (Salt parity): the full highstate — every state set matching the " +
		"peel in `top.zy` — is compiled and applied.",
	Effects: modschema.Effects{
		Execution: "Resolves the peel's settings (falling back to the last-known-good cache on a " +
			"resolve failure, failing closed if the peel has never resolved), then compiles the " +
			"`state` reference — loading its `.zy` file, resolving includes/extends, rendering " +
			"templates against local facts and settings, and building the requisite DAG — and runs " +
			"every compiled state through Check→Apply (Check-only under `test=True`). Returns one " +
			"result per compiled state. Without a `state` argument, behaves exactly as " +
			"`state.highstate` (see its effects). A peel with no states directory yet (KV-only, " +
			"before its first state-file sync) returns a 'states engine unavailable' error.",
	},
	Examples: []modschema.Example{
		{Title: "Apply a state tree", Kind: "cli", Code: "zester '*' state.apply nginx"},
		{Title: "Run the full highstate (Salt parity)", Kind: "cli",
			Explanation: "With no state name, state.apply IS state.highstate; test=true makes it the classic dry run.",
			Code:        "zester '*' state.apply test=true"},
	},
	SeeAlso: []string{"state.highstate"},
}

var docStateHighstate = modschema.Doc{
	Summary: "Compile and apply every state that matches the peel in the state top file.",
	Description: "`state.highstate` resolves the state top file (`top.zy`) for this peel and applies " +
		"every matching state set.",
	Effects: modschema.Effects{
		Execution: "Resolves the peel's settings, evaluates the state top file for this peel, compiles " +
			"every matching state set (includes/extends, template rendering, requisite DAG), and runs " +
			"them through Check→Apply (Check-only under `test=True`). Errors when no state set matches " +
			"the peel in `top.zy`.",
	},
	Examples: []modschema.Example{
		{Title: "Apply the highstate", Kind: "cli", Code: "zester '*' state.highstate"},
	},
	SeeAlso: []string{"state.apply"},
}

var docFactsGet = modschema.Doc{
	Summary: "Read a single fact by dotted key.",
	Effects: modschema.Effects{
		Execution: "Returns the value at the dotted `key` from the peel's live collected facts — a " +
			"scalar verbatim, a composite as trimmed YAML. When the key is absent it returns the " +
			"`default` argument if given, otherwise an empty result. Read-only: it runs on the " +
			"concurrent fast path (outside the mutating exec worker), so it answers even during a " +
			"long state run.",
	},
	Examples: []modschema.Example{
		{Title: "Read the OS family", Kind: "cli", Code: "zester '*' facts.get os.family"},
	},
	SeeAlso: []string{"facts.items", "facts.keys", "facts.set"},
}

var docFactsItems = modschema.Doc{
	Summary: "Dump the entire facts map.",
	Effects: modschema.Effects{
		Execution: "Returns the peel's complete live facts map rendered as trimmed YAML. Read-only " +
			"(concurrent fast path).",
	},
	SeeAlso: []string{"facts.get", "facts.keys"},
}

var docFactsKeys = modschema.Doc{
	Summary: "List the top-level fact key names.",
	Effects: modschema.Effects{
		Execution: "Returns the sorted top-level fact key names, one per line. Read-only (concurrent " +
			"fast path).",
	},
	SeeAlso: []string{"facts.get", "facts.items"},
}

var docFactsSet = modschema.Doc{
	Summary: "Persist a custom fact on the node.",
	Effects: modschema.Effects{
		Execution: "Writes `key`→`value` into the node's custom-facts file (YAML-parsing the value) and " +
			"updates the in-memory facts so subsequent queries and template renders observe it. This is " +
			"the one MUTATING facts.* function: because its custom-facts-file read-modify-write is not " +
			"atomic, it runs ONLY on the serialized exec worker, never the concurrent read-only path.",
	},
	Examples: []modschema.Example{
		{Title: "Set a role fact", Kind: "cli", Code: "zester '*' facts.set key=role value=web"},
	},
	SeeAlso: []string{"facts.get", "facts.items"},
}

var docSettingsGet = modschema.Doc{
	Summary: "Read a single resolved setting by dotted key.",
	Effects: modschema.Effects{
		Execution: "Returns the value at the dotted `key` from the peel's last-resolved settings " +
			"snapshot — a scalar verbatim, a composite as trimmed YAML — or the `default` argument when " +
			"absent. Errors when settings have never resolved on this peel. Read-only (concurrent fast " +
			"path).",
	},
	Examples: []modschema.Example{
		{Title: "Read a setting", Kind: "cli", Code: "zester '*' settings.get app.port"},
	},
	SeeAlso: []string{"settings.items", "settings.keys"},
}

var docSettingsItems = modschema.Doc{
	Summary: "Dump the entire resolved settings map.",
	Effects: modschema.Effects{
		Execution: "Returns the peel's complete resolved settings map rendered as trimmed YAML. Errors " +
			"when settings have never resolved. Read-only (concurrent fast path).",
	},
	SeeAlso: []string{"settings.get", "settings.keys"},
}

var docSettingsKeys = modschema.Doc{
	Summary: "List the top-level resolved settings key names.",
	Effects: modschema.Effects{
		Execution: "Returns the sorted top-level settings key names, one per line. Errors when settings " +
			"have never resolved. Read-only (concurrent fast path).",
	},
	SeeAlso: []string{"settings.get", "settings.items"},
}

var docPillarGet = modschema.Doc{
	Summary: "Salt-compat alias of settings.get.",
	Effects: modschema.Effects{
		Execution: "Salt-compatibility alias: `pillar.get` maps to `settings.get`, reading a dotted " +
			"`key` (or `default`) from the peel's resolved settings snapshot. Read-only.",
	},
	SeeAlso: []string{"settings.get"},
}

var docPillarItems = modschema.Doc{
	Summary: "Salt-compat alias of settings.items.",
	Effects: modschema.Effects{
		Execution: "Salt-compatibility alias: `pillar.items` maps to `settings.items`, dumping the " +
			"resolved settings map as YAML. Read-only.",
	},
	SeeAlso: []string{"settings.items"},
}

var docPillarKeys = modschema.Doc{
	Summary: "Salt-compat alias of settings.keys.",
	Effects: modschema.Effects{
		Execution: "Salt-compatibility alias: `pillar.keys` maps to `settings.keys`, listing the " +
			"top-level settings key names. Read-only.",
	},
	SeeAlso: []string{"settings.keys"},
}

var docEventSend = modschema.Doc{
	Summary: "Publish a custom peel event onto the bus.",
	Description: "`event.send` publishes an event under the peel's own origin, feeding the master " +
		"reactor. The bare positional argument is the tag (slash or dotted); remaining arguments " +
		"become the event data.",
	Effects: modschema.Effects{
		Execution: "Publishes an event.Event on the peel's event subject: the tag comes from the bare " +
			"positional (or `tag=`), the remaining arguments become the event data, and the reactor " +
			"chain depth is threaded from the dispatch. Errors when NATS is down (publishing needs the " +
			"bus). Runs on the serialized (mutating) worker so per-peel event order stays deterministic.",
	},
	Examples: []modschema.Example{
		{Title: "Emit a deployment event", Kind: "cli", Code: "zester '*' event.send app/deployed version=1.2.3"},
	},
}

var docFactsFamily = modschema.Doc{
	Summary: "facts.* prefix-family router.",
	Effects: modschema.Effects{
		Execution: "Routes any `facts.<fn>` request to the facts handler. Recognized functions are " +
			"get/items/keys (read-only) and set (mutating); an unrecognized subfunction returns an " +
			"'unknown facts function' error.",
	},
	SeeAlso: []string{"facts.get", "facts.items", "facts.keys", "facts.set"},
}

var docSettingsFamily = modschema.Doc{
	Summary: "settings.* prefix-family router.",
	Effects: modschema.Effects{
		Execution: "Routes any `settings.<fn>` request to the settings handler (get/items/keys). An " +
			"unrecognized subfunction returns an 'unknown settings function' error.",
	},
	SeeAlso: []string{"settings.get", "settings.items", "settings.keys"},
}

var docPillarFamily = modschema.Doc{
	Summary: "pillar.* prefix-family router (Salt-compat alias of settings.*).",
	Effects: modschema.Effects{
		Execution: "Salt-compatibility alias family: routes any `pillar.<fn>` request to the settings " +
			"handler (get/items/keys). An unrecognized subfunction returns an 'unknown settings " +
			"function' error.",
	},
	SeeAlso: []string{"settings.get", "settings.items", "settings.keys"},
}
