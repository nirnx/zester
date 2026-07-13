package modules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
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
type LocalePresent struct {
	id   string
	reqs state.Requisites

	// Locale is the locale string to enable (e.g. "en_US.UTF-8").
	Locale string

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

// NewLocalePresentBuilder returns a state.Builder that creates LocalePresent states
// using the given ModuleContext's command and file providers.
func NewLocalePresentBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Command == nil {
			return nil, fmt.Errorf("locale.present: no command provider available")
		}
		return newLocalePresent(id, config, mctx.Command, mctx.File)
	}
}

func newLocalePresent(id string, config map[string]any, cmd exec.CommandExec, file exec.FileExec) (state.State, error) {
	l := &LocalePresent{id: id, cmd: cmd, file: file}

	l.Locale, _ = config["name"].(string)
	if l.Locale == "" {
		l.Locale = id
	}

	l.reqs = state.ParseRequisites(config)
	return l, nil
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
	for _, line := range strings.Split(string(data), "\n") {
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
	for _, line := range strings.Split(output, "\n") {
		if normalizeLocale(strings.TrimSpace(line)) == normalised {
			return true
		}
	}
	return false
}
