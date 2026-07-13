package modules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

const localeGenPath = "/etc/locale.gen"

// LocalePresent implements the locale.present state.
// It ensures a locale is enabled in /etc/locale.gen and generated via
// locale-gen. Check verifies BOTH halves — `locale -a` membership and the
// uncommented /etc/locale.gen line — so a locale generated out-of-band still
// converges the file half. The locale.gen facet applies ONLY when
// /etc/locale.gen exists (Debian-family): on systems without it (glibc
// langpacks, musl) the state is satisfied by `locale -a` alone and the file
// is never created. Both halves compare locale names with the same
// normalisation (`locale -a` prints "en_US.utf8" for "en_US.UTF-8"), so
// Check and Apply agree on exactly which line enables the locale.
//
// LocalePresent is also its own schema proto: the single tagged exported
// Locale field IS the module's parameter declaration (one schema declaration
// per module) — `name` is the ONLY parameter, a primitive string primary, so
// no semantic type is needed. Under the uniform decoder a numeric `name`
// coerces to its string form and a composite is rejected (BD-6). The
// unexported runtime fields (id, reqs, cmd, file, revert memo) are untagged,
// so the schema compiler skips them.
type LocalePresent struct {
	id   string
	reqs state.Requisites

	// Locale is the locale string to enable (e.g. "en_US.UTF-8"); it defaults
	// to the state ID.
	Locale string `zester:"name,primary" usage:"locale string to enable (e.g. en_US.UTF-8); defaults to the state ID"`

	cmd  exec.CommandExec
	file exec.FileExec

	// enabledByApply records that a same-instance Apply actually modified
	// /etc/locale.gen (uncommented or appended the locale line), so Revert
	// knows the drift it would undo was applied in this run. Valid only for
	// a same-instance Apply→Revert sequence; a fresh instance (any runner
	// ModeRevert run — states are rebuilt per execution) leaves it false and
	// Revert is an explicit clean no-op — commenting out a line enabled by
	// the installer or an admin is never safe.
	enabledByApply bool
}

// localePresentSpec is the compiled schema + documentation for locale.present.
// It is compiled once at package init and executed by every decode path (the
// builder below, and Registry.Parse). The prose is drift-corrected against the
// live Check/Apply/Revert behavior — notably that /etc/locale.gen is NEVER
// created on a system that lacks it (the hand page wrongly claimed Apply
// creates the file).
var localePresentSpec = mustSpec("locale.present", modschema.KindState, LocalePresent{}, modschema.Doc{
	Summary: "Ensure a locale is enabled in /etc/locale.gen and generated via locale-gen.",
	Description: "`locale.present` ensures a locale (`name`, defaulting to the state ID) is enabled and " +
		"generated. It verifies two independent halves: that `locale -a` lists the locale, and — only on " +
		"systems that have `/etc/locale.gen` (the Debian family) — that the locale's line in that file is " +
		"present and uncommented. Both halves compare locale names case-insensitively with dashes stripped, " +
		"so `en_US.UTF-8` matches the `locale -a` spelling `en_US.utf8`.",
	Effects: modschema.Effects{
		Check: "Runs `locale -a` and reports a change when the locale is absent from its output. When " +
			"present, and only when `/etc/locale.gen` exists, it ALSO verifies the locale's line in that " +
			"file is present and uncommented — a locale generated out-of-band (localedef, image bakery) or " +
			"whose line was later commented out is reported as needing a change, so the next Apply " +
			"re-enables it. On a system without `/etc/locale.gen` (non-Debian: glibc langpacks, musl), " +
			"`locale -a` membership alone satisfies the state.",
		Apply: "On systems with `/etc/locale.gen`: uncomments the locale's existing line, or appends a new " +
			"`<locale> UTF-8` line when none matches — the file itself is NEVER created if it does not " +
			"already exist (a system without it is left without it). Then runs `locale-gen` to regenerate " +
			"the locale database. Reports Changed with the locale in its details.",
		Revert: "Undoes only what this run's Apply actually changed in `/etc/locale.gen` (re-commenting the " +
			"line it uncommented or appended) and re-runs `locale-gen`. A fresh instance (a standalone " +
			"revert) recorded nothing and is an explicit clean no-op — a locale.gen line enabled by the " +
			"distro installer or an admin is never safe to comment out.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Enable a locale",
			Kind:        "state",
			Explanation: "The locale defaults to the state ID.",
			Code:        "en_US.UTF-8:\n  locale.present:\n    - require:\n      - pkg.installed:locales\n",
		},
		{
			Title:       "Enable a second locale in order",
			Kind:        "state",
			Explanation: "require orders one locale.gen edit after another (here, de_DE.UTF-8) to avoid concurrent rewrites.",
			Code:        "fr_FR.UTF-8:\n  locale.present:\n    - require:\n      - \"locale.present:de_DE.UTF-8\"\n",
		},
		{
			Title:       "Enable a locale ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the locale string.",
			Code:        "zester '*' locale.present en_US.UTF-8",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Debian/Ubuntu only for the locale.gen facet",
			Body: "This module targets systems that use `/etc/locale.gen` and `locale-gen` (Debian, " +
				"Ubuntu). On Red Hat-based systems, locale management uses `localectl` instead — use " +
				"`cmd.run` with `localectl set-locale` there. The `locale` binary and `locale-gen` must be " +
				"available on the target (the `locales` package on Debian/Ubuntu).",
		},
		{
			Level: "info",
			Title: "locale.gen is never created from scratch",
			Body: "On a system that already has `/etc/locale.gen`, a missing locale line is APPENDED " +
				"(with a default `UTF-8` encoding suffix). On a system without the file at all, it is " +
				"never created — the locale.gen facet simply does not apply there, and the state is " +
				"satisfied by `locale -a` membership alone.",
		},
	},
	Divergences: []string{"BD-6"},
})

// NewLocalePresentBuilder returns a state.Builder that creates LocalePresent
// states using the given ModuleContext's command and file providers. Decode
// policy (unknown-key handling, reserved keys) is threaded via opts; the peel
// supplies it through modules.RegisterAll.
func NewLocalePresentBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Command == nil {
			return nil, fmt.Errorf("locale.present: no command provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected providers, id, and
		// requisites MUST be assigned AFTER it — assigning them before would be
		// overwritten by the committed scratch value.
		l := &LocalePresent{}
		if _, err := localePresentSpec.Decode(id, config, l, opts); err != nil {
			return nil, fmt.Errorf("locale.present: %w", err)
		}
		l.id = id
		l.cmd = mctx.Command
		l.file = mctx.File
		l.reqs = state.ParseRequisites(config)
		return l, nil
	}
}

func (l *LocalePresent) Name() string           { return "locale.present:" + l.id }
func (l *LocalePresent) Reqs() state.Requisites { return l.reqs }

func (l *LocalePresent) Check(ctx context.Context) (state.CheckResult, error) {
	result, err := l.cmd.Run(ctx, exec.CommandOpts{
		Command: "locale",
		Args:    []string{"-a"},
	})
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("locale.present: list locales: %w", err)
	}

	if !localeInOutput(result.Stdout, l.Locale) {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("locale %s is not generated", l.Locale),
		}, nil
	}

	// Generated — also verify the enabling line in /etc/locale.gen, the other
	// half of what Apply enforces. A locale generated out-of-band (localedef,
	// image bakery) or whose line was later commented out would otherwise
	// report compliant forever, and the next external locale-gen run (e.g. a
	// locales package upgrade) would silently drop it. The facet applies ONLY
	// when the file exists: a system without /etc/locale.gen (non-Debian) is
	// satisfied by `locale -a` alone — exactly what Apply enforces there.
	if l.file != nil {
		enabled, exists, err := l.localeGenEnabled(ctx)
		if err != nil {
			return state.CheckResult{}, err
		}
		if exists && !enabled {
			return state.CheckResult{
				NeedsChange: true,
				Diff:        fmt.Sprintf("locale %s is generated but not enabled in %s", l.Locale, localeGenPath),
			}, nil
		}
	}

	return state.CheckResult{
		NeedsChange: false,
		Diff:        fmt.Sprintf("locale %s is already generated", l.Locale),
	}, nil
}

// localeGenEnabled reports whether /etc/locale.gen exists and contains an
// uncommented line enabling the locale (the line Apply enforces).
// exists=false (only on fs.ErrNotExist) means the file facet does not apply
// on this system — the file is NEVER created on systems that don't have it;
// any other read error fails the phase.
func (l *LocalePresent) localeGenEnabled(ctx context.Context) (enabled, exists bool, err error) {
	data, err := l.file.ReadFile(ctx, localeGenPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("locale.present: read %s: %w", localeGenPath, err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if localeGenLineMatches(trimmed, l.Locale) {
			return true, true, nil
		}
	}
	return false, true, nil
}

func (l *LocalePresent) Apply(ctx context.Context) (state.ApplyResult, error) {
	if l.file != nil {
		changed, err := l.enableLocaleInFile(ctx, false)
		if err != nil {
			return state.ApplyResult{}, err
		}
		if changed {
			l.enabledByApply = true
		}
	}

	if _, err := l.cmd.Run(ctx, exec.CommandOpts{
		Command: "locale-gen",
	}); err != nil {
		return state.ApplyResult{}, fmt.Errorf("locale.present: locale-gen: %w", err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("generated locale %s", l.Locale),
		Details: map[string]string{
			"locale": l.Locale,
		},
	}, nil
}

func (l *LocalePresent) Revert(ctx context.Context) (state.ApplyResult, error) {
	// Standalone-Revert contract: without the same-instance memo there is no
	// evidence THIS run enabled the locale — the /etc/locale.gen line may
	// have been enabled by the distro installer or an admin, and commenting
	// it out (then running locale-gen) would drop a locale zester never
	// added. Fresh instances (any runner ModeRevert) are a clean no-op.
	if !l.enabledByApply {
		return state.ApplyResult{
			Changed: false,
			Diff:    "locale.present: nothing to revert (no apply recorded in this run)",
		}, nil
	}

	if _, err := l.enableLocaleInFile(ctx, true); err != nil {
		return state.ApplyResult{}, err
	}

	if _, err := l.cmd.Run(ctx, exec.CommandOpts{
		Command: "locale-gen",
	}); err != nil {
		return state.ApplyResult{}, fmt.Errorf("locale.present: locale-gen revert: %w", err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("disabled locale %s", l.Locale),
	}, nil
}

// enableLocaleInFile edits /etc/locale.gen to uncomment (comment=false) or
// comment out (comment=true) the locale line, reporting whether the file was
// actually modified. Line matching uses the same normalisation as Check's
// localeGenEnabled, so what Apply writes is exactly what Check accepts.
// A missing file means the facet does not apply on this system — it is never
// created (fs.ErrNotExist = clean skip); any other read error fails the
// phase — overwriting an unreadable locale.gen would silently drop every
// other enabled locale.
func (l *LocalePresent) enableLocaleInFile(ctx context.Context, comment bool) (bool, error) {
	data, err := l.file.ReadFile(ctx, localeGenPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("locale.present: read %s: %w", localeGenPath, err)
	}

	lines := strings.Split(string(data), "\n")
	changed := false
	found := false
	for i, line := range lines {
		stripped := strings.TrimLeft(line, "# ")
		if !localeGenLineMatches(stripped, l.Locale) {
			continue
		}
		found = true
		isCommented := strings.HasPrefix(strings.TrimSpace(line), "#")
		if comment && !isCommented {
			lines[i] = "# " + stripped
			changed = true
		} else if !comment && isCommented {
			lines[i] = stripped
			changed = true
		}
		break
	}

	if !found && !comment {
		lines = append(lines, l.Locale+" UTF-8")
		changed = true
	}

	if !changed {
		return false, nil
	}
	if err := l.file.WriteFile(ctx, localeGenPath, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		return false, fmt.Errorf("locale.present: write %s: %w", localeGenPath, err)
	}
	return true, nil
}

// normalizeLocale canonicalises a locale name the way `locale -a` output is
// compared: lowercase with dashes stripped, so "en_US.UTF-8", "en_US.utf8"
// and "en_US.UTF8" all denote the same locale.
func normalizeLocale(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "-", ""))
}

// localeGenLineMatches reports whether a locale.gen line's locale field (the
// first whitespace-separated token, e.g. "en_US.UTF-8" of
// "en_US.UTF-8 UTF-8") denotes the declared locale. It uses the SAME
// normalisation as the `locale -a` half, so a state declared as "en_US.utf8"
// (the spelling `locale -a` prints) matches the canonical locale.gen line
// instead of driving a spurious Apply that appends a junk duplicate.
func localeGenLineMatches(line, locale string) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	return normalizeLocale(fields[0]) == normalizeLocale(locale)
}

// localeInOutput checks whether the target locale appears in `locale -a` output.
// It normalises the locale name for comparison (e.g. "en_US.utf8" vs "en_US.UTF-8").
func localeInOutput(output, locale string) bool {
	normalised := normalizeLocale(locale)
	for line := range strings.SplitSeq(output, "\n") {
		if normalizeLocale(strings.TrimSpace(line)) == normalised {
			return true
		}
	}
	return false
}
