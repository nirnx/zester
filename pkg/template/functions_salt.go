package template

import (
	"fmt"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	gonjaExec "github.com/nikolalohinski/gonja/v2/exec"
)

// registerSaltFunctions adds Salt-compatible functions to the template context.
// These are registered per-render in buildContext() because they need access
// to the current render's facts and settings.
func registerSaltFunctions(m map[string]any, facts, stgs map[string]any) {
	m["pillar_get"] = makePillarGet(stgs)
	m["grains_filter_by"] = makeGrainsFilterBy(facts)
	m["user_info"] = userInfo
	m["cmd_has_exec"] = cmdHasExec
	m["file_dirname"] = fileDirname
}

// makePillarGet creates pillar_get(key, default) which does colon-separated
// nested lookup on settings, matching Salt's pillar.get behavior.
// Example: pillar_get('users:john:shell', '/bin/bash')
func makePillarGet(stgs map[string]any) func(key string, args ...any) any {
	return func(key string, args ...any) any {
		parts := strings.Split(key, ":")
		var current any = stgs
		for _, part := range parts {
			cm, ok := current.(map[string]any)
			if !ok {
				if len(args) > 0 {
					return args[0]
				}
				return nil
			}
			current, ok = cm[part]
			if !ok {
				if len(args) > 0 {
					return args[0]
				}
				return nil
			}
		}
		return current
	}
}

// makeGrainsFilterBy creates grains_filter_by(lookup_dict, grain?, merge?, base?)
// which selects a config dict from lookup_dict based on the value of a fact (grain),
// then deep-merges with optional base and merge dicts. Matches Salt's grains.filter_by.
//
// Uses gonja's *VarArgs to safely handle nil values (pillar_get returning nil
// when a key doesn't exist) — Go reflect.ValueOf(nil) is invalid and causes
// gonja's evalParams to fail, but VarArgs wraps everything in *Value safely.
func makeGrainsFilterBy(facts map[string]any) func(*gonjaExec.VarArgs) any {
	return func(va *gonjaExec.VarArgs) any {
		if len(va.Args) < 1 {
			return nil
		}

		// First arg: lookup dict
		lookupRaw := va.Args[0].ToGoSimpleType(false)
		lookupDict, ok := lookupRaw.(map[string]any)
		if !ok {
			return nil
		}

		// Second arg (optional): grain key, or a dict (merge) in some Salt patterns
		grainKey := "os.family"
		var extraMerge map[string]any
		if len(va.Args) > 1 && !va.Args[1].IsNil() {
			if va.Args[1].IsString() {
				if s := va.Args[1].String(); s != "" {
					grainKey = s
				}
			} else if va.Args[1].IsDict() {
				// Second arg is a dict — Salt patterns sometimes pass the merge
				// dict as the second positional arg (skipping grain key).
				raw := va.Args[1].ToGoSimpleType(false)
				if m, ok := raw.(map[string]any); ok {
					extraMerge = m
				}
			}
		}

		// Look up the grain value
		grainValue := lookupFactKey(facts, grainKey)
		if grainValue == nil {
			// Try "default" key in lookup dict
			result, ok := lookupDict["default"]
			if !ok {
				return nil
			}
			resultMap, ok := result.(map[string]any)
			if !ok {
				return result
			}
			if extraMerge != nil {
				resultMap = deepMerge(resultMap, extraMerge)
			}
			return resultMap
		}

		grainStr := fmt.Sprintf("%v", grainValue)

		// Select from lookup dict
		result, ok := lookupDict[grainStr]
		if !ok {
			result, ok = lookupDict["default"]
			if !ok {
				return nil
			}
		}

		resultMap, ok := result.(map[string]any)
		if !ok {
			return result
		}

		// Apply extra merge from second-arg dict pattern
		if extraMerge != nil {
			resultMap = deepMerge(resultMap, extraMerge)
		}

		// Apply merge dict (3rd arg) if present and is a dict
		if len(va.Args) > 2 && !va.Args[2].IsNil() && va.Args[2].IsDict() {
			raw := va.Args[2].ToGoSimpleType(false)
			if mergeMap, ok := raw.(map[string]any); ok {
				resultMap = deepMerge(resultMap, mergeMap)
			}
		}

		// Apply base dict (4th arg) if present and is a dict — base goes under
		if len(va.Args) > 3 && !va.Args[3].IsNil() && va.Args[3].IsDict() {
			raw := va.Args[3].ToGoSimpleType(false)
			if baseMap, ok := raw.(map[string]any); ok {
				resultMap = deepMerge(baseMap, resultMap)
			}
		}

		return resultMap
	}
}

// userInfo looks up a system user by name and returns a map with their info.
// Matches Salt's user.info function.
func userInfo(name string) map[string]any {
	u, err := user.Lookup(name)
	if err != nil {
		return map[string]any{}
	}
	result := map[string]any{
		"name":  u.Username,
		"uid":   u.Uid,
		"gid":   u.Gid,
		"home":  u.HomeDir,
		"shell": "",
	}

	gids, err := u.GroupIds()
	if err == nil {
		groups := make([]string, 0, len(gids))
		for _, gid := range gids {
			if g, err := user.LookupGroupId(gid); err == nil {
				groups = append(groups, g.Name)
			} else {
				groups = append(groups, gid)
			}
		}
		result["groups"] = groups
	} else {
		result["groups"] = []string{}
	}

	return result
}

// cmdHasExec checks if a command is available in the system PATH.
// Matches Salt's cmd.has_exec function.
func cmdHasExec(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// fileDirname returns the directory component of a path.
// Matches Salt's file.dirname function.
func fileDirname(path string) string {
	return filepath.Dir(path)
}

// deepMerge recursively merges overlay into base. Maps are merged recursively;
// all other types are overwritten by overlay. This is the same algorithm as
// settings.MergeSettings but inlined to avoid an import cycle
// (pkg/settings imports pkg/template).
func deepMerge(base, overlay map[string]any) map[string]any {
	result := make(map[string]any, len(base))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range overlay {
		if baseMap, ok := result[k].(map[string]any); ok {
			if overlayMap, ok := v.(map[string]any); ok {
				result[k] = deepMerge(baseMap, overlayMap)
				continue
			}
		}
		result[k] = v
	}
	return result
}
