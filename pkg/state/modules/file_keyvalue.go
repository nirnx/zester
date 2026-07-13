package modules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"regexp"
	"sort"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/paramtypes"
	"github.com/nirnx/zester/pkg/state"
)

// FileKeyValue implements the file.keyvalue state.
// It ensures one or more "key<separator>value" lines are present in a file,
// updating the value in place when the key already exists (e.g. sysctl.conf,
// os-release, environment files).
//
// FileKeyValue is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module). `key_values`
// and `entries` are TWO separate paramtypes.StringMap parameters — NOT a single
// name-wins alias: the legacy constructor unioned both maps (`entries` winning a
// per-key collision, since it merged second), and a name-wins alias would have
// silently discarded `entries` whenever both were supplied. Each map decodes on
// its own — a scalar value is rendered to a string, but a COMPOSITE value (a
// nested map/list) is a typed error rather than being sprint'd into Go syntax
// (BD-5). The single `key`/`value` pair keeps its legacy single-entry injection
// as a module-local post-decode merge (spec-sanctioned): `key` requires a
// present, non-nil `value`. The merged desired map lives in the untagged runtime
// field Entries (a paramtypes.StringMap so contract projection compares it
// through the StringMap matcher; untagged, so the schema compiler skips it). The
// remaining unexported runtime fields are untagged.
type FileKeyValue struct {
	id   string
	reqs state.Requisites

	// Path is the file to edit; it defaults to the state ID.
	Path string `zester:"name,primary,aliases=path" usage:"absolute path to the target file; the path alias is accepted for Salt compatibility; defaults to the state ID"`

	// Separator is the literal token written between key and value (default "=").
	Separator string `zester:"separator,default==" usage:"literal token written between key and value; defaults to ="`

	// KeyValues is Salt's key_values map. It is unioned with EntriesInput (below);
	// on a per-key collision EntriesInput wins (legacy merge order).
	KeyValues paramtypes.StringMap `zester:"key_values" usage:"map of keys to their desired values (Salt's key_values); unioned with entries, which wins a per-key collision"`

	// EntriesInput is the `entries` map — a SEPARATE parameter (not a name-wins
	// alias of key_values). Both maps are unioned; on a per-key collision the
	// entries value wins (it merges after key_values, matching the legacy order).
	EntriesInput paramtypes.StringMap `zester:"entries" usage:"map of keys to their desired values; unioned with key_values, winning a per-key collision"`

	// Key/Value are the single-entry convenience form (merged into Entries after
	// decode, overwriting a colliding map entry). Key requires a present, non-nil
	// Value.
	Key   string `zester:"key" usage:"a single key to manage (used with value); merged into the entries map"`
	Value string `zester:"value" usage:"the desired value for key; required when key is set"`

	// Entries is the merged desired key→value map (KeyValues ∪ EntriesInput, then
	// the single Key/Value injection). Untagged (not a user parameter directly),
	// so the schema compiler skips it; exported for test/contract projection.
	Entries paramtypes.StringMap

	file exec.FileExec

	backup    []byte
	backupSet bool
	created   bool
}

// fileKeyValueSpec is the compiled schema + documentation for file.keyvalue. It
// is compiled once at package init and executed by every decode path (the
// builder below, and Registry.Parse). The prose is verified against the live
// Check/Apply/Revert code (notably: an existing key's value is updated in place;
// a new key is appended; and the separator is matched trimmed so surrounding
// spaces are tolerated).
var fileKeyValueSpec = mustSpec("file.keyvalue", modschema.KindState, FileKeyValue{}, modschema.Doc{
	Summary: "Ensure key/value lines are present in a file, updating values in place.",
	Description: "`file.keyvalue` ensures one or more `key<separator>value` lines exist in a file, " +
		"updating the value in place when the key already exists (for `sysctl.conf`, `os-release`, or " +
		"environment files). The path defaults to the state ID (the `path` alias is accepted for Salt " +
		"compatibility). Supply the pairs as a `key_values` map (Salt's name), an `entries` map, a single " +
		"`key`/`value` pair, or any combination — `key_values` and `entries` are UNIONED (they are two " +
		"separate parameters, not aliases), with `entries` winning a per-key collision, and the single " +
		"`key`/`value` pair overriding both for its key. The `separator` defaults to `=` and is matched " +
		"trimmed, so an existing `k = v` line (with surrounding spaces) is recognized and updated rather than " +
		"duplicated.",
	Effects: modschema.Effects{
		Check: "Reads the file (a missing file needs a change — it will be created; any other read error " +
			"fails the check) and computes the result in memory: a change is needed when any managed key is " +
			"absent or its current value differs from the desired one.",
		Apply: "Reads the file (capturing the original for revert on the first write when it already " +
			"exists). For each key, updates the matching line's value in place (preserving its leading " +
			"whitespace) or appends `key<separator>value` when absent, then writes the result with mode 0644 " +
			"(preserving the trailing newline). Creates the file with the managed lines when it does not exist.",
		Revert: "Restores what this run's Apply changed: a file that pre-existed is rewritten with its " +
			"captured prior content; a file this instance created is removed (tolerating an already-missing " +
			"file). A fresh instance (a standalone revert) recorded nothing and is an explicit clean no-op.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Set kernel parameters",
			Kind:        "state",
			Explanation: "key_values sets multiple entries; an existing line's value is updated in place.",
			Code: "/etc/sysctl.conf:\n  file.keyvalue:\n    - key_values:\n" +
				"        net.ipv4.ip_forward: \"1\"\n        vm.swappiness: \"10\"\n",
		},
		{
			Title:       "Set a single value with a custom separator",
			Kind:        "state",
			Explanation: "key/value is the single-entry form; separator overrides the default = (a spaced separator here).",
			Code: "/etc/login.defs:\n  file.keyvalue:\n    - key: UMASK\n    - value: \"027\"\n" +
				"    - separator: \"   \"\n",
		},
		{
			Title:       "Set a value ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional is the path; key and value are key=value args.",
			Code:        "zester 'web*' file.keyvalue /etc/os-release key=PRETTY_NAME value='Zester Linux'",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Separator matching tolerates spaces",
			Body: "The separator is matched with its surrounding whitespace trimmed, so an existing " +
				"`key = value` line is recognized and updated rather than a duplicate `key=value` line being " +
				"appended. The written line uses the separator exactly as configured.",
		},
		{
			Level: "info",
			Title: "key_values and entries are unioned",
			Body: "`key_values` (Salt's name) and `entries` are two SEPARATE parameters, not aliases. When " +
				"both are supplied their maps are unioned; on a per-key collision the `entries` value wins " +
				"(it merges after `key_values`). A single `key`/`value` pair is merged last, overriding both " +
				"for its key.",
		},
		{
			Level: "info",
			Title: "Divergences from Salt",
			Body: "Keys that are not found are always appended — Salt requires `append_if_not_found: True` " +
				"for that, but Zester has no such flag. Only the FIRST matching line per key is updated; " +
				"duplicate lines further down are left untouched. Salt's `count`, `uncomment`, " +
				"`key_ignore_case`, and `value_ignore_case` parameters are not supported.",
		},
	},
	Divergences: []string{"BD-5", "BD-6"},
	SeeAlso:     []string{"file.line", "file.replace", "sysctl.present"},
})

// NewFileKeyValueBuilder returns a state.Builder that creates FileKeyValue
// states. Decode policy (unknown-key handling, reserved keys) is threaded via
// opts; the peel supplies it through modules.RegisterAll.
func NewFileKeyValueBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file.keyvalue: no file provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the derived Entries map, injected
		// provider, id, and requisites MUST be assigned AFTER it.
		f := &FileKeyValue{}
		if _, err := fileKeyValueSpec.Decode(id, config, f, opts); err != nil {
			return nil, fmt.Errorf("file.keyvalue: %w", err)
		}

		// Merge the desired map in the legacy union order: key_values first, then
		// entries (which wins a per-key collision), then the single key/value
		// injection (which overwrites both for its key). Reading config["value"]
		// directly preserves the legacy "key requires a present, non-nil value"
		// contract (an empty-string value is allowed; absent or nil is an error).
		f.Entries = paramtypes.StringMap{}
		maps.Copy(f.Entries, f.KeyValues)
		maps.Copy(f.Entries, f.EntriesInput)
		if f.Key != "" {
			v, present := config["value"]
			if !present || v == nil {
				return nil, fmt.Errorf("file.keyvalue: key %q requires a value", f.Key)
			}
			f.Entries[f.Key] = f.Value
		}
		if len(f.Entries) == 0 {
			return nil, fmt.Errorf("file.keyvalue: no entries (use key/value, key_values, or entries)")
		}

		f.id = id
		f.file = mctx.File
		f.reqs = state.ParseRequisites(config)
		return f, nil
	}
}

func (f *FileKeyValue) Name() string           { return "file.keyvalue:" + f.id }
func (f *FileKeyValue) Reqs() state.Requisites { return f.reqs }

func (f *FileKeyValue) sortedKeys() []string {
	keys := make([]string, 0, len(f.Entries))
	for k := range f.Entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// fsxKeyValueRegex builds a matcher for an existing "key <sep> value" line,
// capturing the leading whitespace (group 1) and current value (group 2). The
// separator is matched trimmed so surrounding spaces are tolerated.
func fsxKeyValueRegex(key, sep string) *regexp.Regexp {
	s := trimSpaceASCII(sep)
	if s == "" {
		s = "="
	}
	return regexp.MustCompile(`^(\s*)` + regexp.QuoteMeta(key) + `\s*` + regexp.QuoteMeta(s) + `\s*(.*?)\s*$`)
}

// trimSpaceASCII trims spaces and tabs from both ends of s.
func trimSpaceASCII(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

// compute returns the new lines and whether anything changed.
func (f *FileKeyValue) compute(lines []string) ([]string, bool) {
	out := append([]string(nil), lines...)
	changed := false

	for _, key := range f.sortedKeys() {
		val := f.Entries[key]
		re := fsxKeyValueRegex(key, f.Separator)
		found := false
		for i, l := range out {
			m := re.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			found = true
			newLine := m[1] + key + f.Separator + val
			if out[i] != newLine {
				out[i] = newLine
				changed = true
			}
			break
		}
		if !found {
			out = append(out, key+f.Separator+val)
			changed = true
		}
	}

	return out, changed
}

func (f *FileKeyValue) Check(ctx context.Context) (state.CheckResult, error) {
	data, err := f.file.ReadFile(ctx, f.Path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return state.CheckResult{}, fmt.Errorf("file.keyvalue: read %s: %w", f.Path, err)
		}
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("file %s does not exist", f.Path),
		}, nil
	}

	lines, _ := fsxSplitLines(string(data))
	_, changed := f.compute(lines)
	if !changed {
		return state.CheckResult{NeedsChange: false}, nil
	}
	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("%d key/value entries pending in %s", len(f.Entries), f.Path),
	}, nil
}

func (f *FileKeyValue) Apply(ctx context.Context) (state.ApplyResult, error) {
	data, err := f.file.ReadFile(ctx, f.Path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return state.ApplyResult{}, fmt.Errorf("file.keyvalue: read %s: %w", f.Path, err)
	}
	existed := err == nil
	var lines []string
	trailingNL := true
	if existed {
		if !f.backupSet {
			f.backup = data
			f.backupSet = true
		}
		lines, trailingNL = fsxSplitLines(string(data))
	}

	newLines, changed := f.compute(lines)
	if !changed {
		return state.ApplyResult{Changed: false}, nil
	}

	out := fsxJoinLines(newLines, trailingNL)
	if err := f.file.WriteFile(ctx, f.Path, []byte(out), 0644); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.keyvalue: write %s: %w", f.Path, err)
	}
	if !existed {
		f.created = true
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("ensured %d key/value entries in %s", len(f.Entries), f.Path),
		Details: map[string]string{"path": f.Path, "entries": fmt.Sprintf("%d", len(f.Entries))},
	}, nil
}

func (f *FileKeyValue) Revert(ctx context.Context) (state.ApplyResult, error) {
	return fsxRevertWithCreate(ctx, f.file, f.Path, f.backup, f.backupSet, f.created, "file.keyvalue")
}
