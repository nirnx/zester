package migrate

import (
	"fmt"
	"regexp"
	"strings"
)

// --- Rule 1: Salt Function Calls ---

// saltFuncMap maps Salt module.function names to their Zester equivalents.
var saltFuncMap = map[string]string{
	"pillar.get":       "pillar_get",
	"grains.filter_by": "grains_filter_by",
	"user.info":        "user_info",
	"cmd.has_exec":     "cmd_has_exec",
	"file.dirname":     "file_dirname",
}

var saltFuncRe = regexp.MustCompile(`salt\['(pillar\.get|grains\.filter_by|user\.info|cmd\.has_exec|file\.dirname)'\]\s*\(`)

func saltFunctions(lines []string, r *Result) {
	for i, line := range lines {
		if !strings.Contains(line, "salt[") {
			continue
		}
		newLine := saltFuncRe.ReplaceAllStringFunc(line, func(match string) string {
			sub := saltFuncRe.FindStringSubmatch(match)
			if len(sub) < 2 {
				return match
			}
			return saltFuncMap[sub[1]] + "("
		})
		if newLine != line {
			r.Changes = append(r.Changes, Change{
				Line:   i + 1,
				Before: line,
				After:  newLine,
				Rule:   "salt-functions",
			})
			lines[i] = newLine
		}
	}
}

// --- Rule 2: Unknown Salt Calls ---

var unknownSaltRe = regexp.MustCompile(`salt\['[\w.]+'\]`)

func unknownSaltCalls(lines []string, r *Result) {
	for i, line := range lines {
		matches := unknownSaltRe.FindAllString(line, -1)
		for _, m := range matches {
			r.Warnings = append(r.Warnings, Warning{
				Line:    i + 1,
				Message: fmt.Sprintf("Unknown salt call: %s", m),
				Context: line,
			})
		}
	}
}

// --- Rule 3: Mutable List Detection ---

// emptyListSetRe matches {% set NAME = [] %}
var emptyListSetRe = regexp.MustCompile(`\{%[-\s]*set\s+(\w+)\s*=\s*\[\]\s*[-\s]*%\}`)

func mlistDetection(lines []string, r *Result) {
	// First pass: find variables initialized as empty lists.
	listVars := map[string]int{} // name → line index
	for i, line := range lines {
		m := emptyListSetRe.FindStringSubmatch(line)
		if m != nil {
			listVars[m[1]] = i
		}
	}
	// Determine which declared vars actually use .append() or .extend().
	usedVars := map[string]bool{}
	for name := range listVars {
		appendPat := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\.append\(`)
		extendPat := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\.extend\(`)
		for _, line := range lines {
			if appendPat.MatchString(line) || extendPat.MatchString(line) {
				usedVars[name] = true
				break
			}
		}
	}

	// Second pass: transform declarations and method calls for used vars.
	for name := range usedVars {
		declIdx := listVars[name]
		oldDecl := lines[declIdx]
		newDecl := emptyListSetRe.ReplaceAllStringFunc(oldDecl, func(match string) string {
			sub := emptyListSetRe.FindStringSubmatch(match)
			if sub[1] == name {
				return strings.Replace(match, "[]", "mlist()", 1)
			}
			return match
		})
		if newDecl != oldDecl {
			r.Changes = append(r.Changes, Change{
				Line:   declIdx + 1,
				Before: oldDecl,
				After:  newDecl,
				Rule:   "mlist",
			})
			lines[declIdx] = newDecl
		}

		appendPat := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\.append\(`)
		extendPat := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\.extend\(`)
		for i, line := range lines {
			newLine := appendPat.ReplaceAllString(line, name+".Append(")
			newLine = extendPat.ReplaceAllString(newLine, name+".Extend(")
			if newLine != line {
				r.Changes = append(r.Changes, Change{
					Line:   i + 1,
					Before: line,
					After:  newLine,
					Rule:   "mlist",
				})
				lines[i] = newLine
			}
		}
	}

	// Warn about .append()/.extend() on variables NOT declared as [].
	appendAnyRe := regexp.MustCompile(`\b(\w+)\.append\(`)
	extendAnyRe := regexp.MustCompile(`\b(\w+)\.extend\(`)
	for i, line := range lines {
		for _, re := range []*regexp.Regexp{appendAnyRe, extendAnyRe} {
			matches := re.FindAllStringSubmatch(line, -1)
			for _, m := range matches {
				varName := m[1]
				if _, declared := listVars[varName]; !declared {
					r.Warnings = append(r.Warnings, Warning{
						Line:    i + 1,
						Message: fmt.Sprintf(".append()/.extend() on %q which was not declared as []; may need manual mlist() conversion", varName),
						Context: line,
					})
				}
			}
		}
	}
}

// --- Rule 4: Octal Mode Quoting ---

var octalModeRe = regexp.MustCompile(`(-\s*(?:mode|dir_mode|file_mode):\s*)(0[0-7]{3,4})\s*$`)

func octalModes(lines []string, r *Result) {
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "mode") {
			continue
		}
		newLine := octalModeRe.ReplaceAllString(line, `${1}"${2}"`)
		if newLine != line {
			r.Changes = append(r.Changes, Change{
				Line:   i + 1,
				Before: line,
				After:  newLine,
				Rule:   "octal-modes",
			})
			lines[i] = newLine
		}
	}
}

// --- Rule 5: Requisite Format Warnings ---

// reqHeaderRe matches every requisite type Zester supports, including the
// inverse (_in) forms, prereq, and listen (all natively supported).
var reqHeaderRe = regexp.MustCompile(`^(\s*)-\s*(require|watch|onchanges|onfail|prereq|listen|require_in|watch_in|onchanges_in|onfail_in|prereq_in|listen_in):\s*$`)
var reqEntryRe = regexp.MustCompile(`^\s*-\s*(\w+):\s*(.+)`)

// saltModuleToZester maps Salt requisite shorthand modules to Zester full module names.
var saltModuleToZester = map[string]string{
	"pkg":     "pkg.installed",
	"file":    "file.managed",
	"service": "service.running",
	"cmd":     "cmd.run",
	"user":    "user.present",
	"group":   "group.present",
}

func requisiteWarnings(lines []string, r *Result) {
	for i := 0; i < len(lines); i++ {
		hm := reqHeaderRe.FindStringSubmatch(lines[i])
		if hm == nil {
			continue
		}

		indent := hm[1]
		reqType := hm[2]
		startLine := i

		// Collect entries that follow this requisite header.
		var entries []string
		j := i + 1
		for j < len(lines) {
			em := reqEntryRe.FindStringSubmatch(lines[j])
			if em == nil {
				break
			}
			saltMod := em[1]
			stateID := strings.TrimSpace(em[2])
			zesterMod := saltModuleToZester[saltMod]
			if zesterMod == "" {
				zesterMod = saltMod + ".managed" // fallback
			}
			entries = append(entries, fmt.Sprintf("%s    - %q", indent, zesterMod+":"+stateID))
			j++
		}

		if len(entries) == 0 {
			continue
		}

		suggestion := fmt.Sprintf("%s- %s:\n%s", indent, reqType, strings.Join(entries, "\n"))

		r.Warnings = append(r.Warnings, Warning{
			Line:    startLine + 1,
			Message: fmt.Sprintf("Salt-style requisite block. Suggested Zester format:\n%s", suggestion),
			Context: lines[startLine],
		})

		i = j - 1 // skip past the entries we already consumed
	}
}

// --- Rule 6: Variable Rename (optional) ---

var (
	grainsAccessRe = regexp.MustCompile(`\bgrains([.\[])`)
	pillarAccessRe = regexp.MustCompile(`\bpillar([.\[])`)
)

func variableRename(lines []string, r *Result) {
	for i, line := range lines {
		newLine := grainsAccessRe.ReplaceAllStringFunc(line, func(match string) string {
			// Don't rename if it's part of grains_filter_by
			idx := strings.Index(line, match)
			rest := line[idx:]
			if strings.HasPrefix(rest, "grains_") {
				return match
			}
			return "facts" + match[len("grains"):]
		})
		newLine = pillarAccessRe.ReplaceAllStringFunc(newLine, func(match string) string {
			idx := strings.Index(newLine, match)
			rest := newLine[idx:]
			if strings.HasPrefix(rest, "pillar_") {
				return match
			}
			return "settings" + match[len("pillar"):]
		})
		if newLine != line {
			r.Changes = append(r.Changes, Change{
				Line:   i + 1,
				Before: line,
				After:  newLine,
				Rule:   "variable-rename",
			})
			lines[i] = newLine
		}
	}
}
