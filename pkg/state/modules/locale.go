package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

const localeGenPath = "/etc/locale.gen"

// LocalePresent implements the locale.present state.
// It ensures a locale is enabled in /etc/locale.gen and generated via locale-gen.
type LocalePresent struct {
	id   string
	reqs state.Requisites

	// Locale is the locale string to enable (e.g. "en_US.UTF-8").
	Locale string

	cmd  exec.CommandExec
	file exec.FileExec
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

	if localeInOutput(result.Stdout, l.Locale) {
		return state.CheckResult{
			NeedsChange: false,
			Diff:        fmt.Sprintf("locale %s is already generated", l.Locale),
		}, nil
	}

	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("locale %s is not generated", l.Locale),
	}, nil
}

func (l *LocalePresent) Apply(ctx context.Context) (state.ApplyResult, error) {
	if l.file != nil {
		if err := l.enableLocaleInFile(ctx, false); err != nil {
			return state.ApplyResult{}, err
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
	if l.file != nil {
		if err := l.enableLocaleInFile(ctx, true); err != nil {
			return state.ApplyResult{}, err
		}
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

// enableLocaleInFile edits /etc/locale.gen to uncomment or comment the locale line.
// When comment=true the locale is commented out (for Revert); when false it is uncommented.
func (l *LocalePresent) enableLocaleInFile(ctx context.Context, comment bool) error {
	data, err := l.file.ReadFile(ctx, localeGenPath)
	if err != nil {
		// File doesn't exist — create it with the locale line.
		if !comment {
			return l.file.WriteFile(ctx, localeGenPath, []byte(l.Locale+" UTF-8\n"), 0644)
		}
		return nil
	}

	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		stripped := strings.TrimLeft(line, "# ")
		if strings.HasPrefix(stripped, l.Locale) {
			if comment {
				lines[i] = "# " + stripped
			} else {
				lines[i] = stripped
			}
			found = true
			break
		}
	}

	if !found && !comment {
		lines = append(lines, l.Locale+" UTF-8")
	}

	return l.file.WriteFile(ctx, localeGenPath, []byte(strings.Join(lines, "\n")), 0644)
}

// localeInOutput checks whether the target locale appears in `locale -a` output.
// It normalises the locale name for comparison (e.g. "en_US.utf8" vs "en_US.UTF-8").
func localeInOutput(output, locale string) bool {
	normalised := strings.ToLower(strings.ReplaceAll(locale, "-", ""))
	for _, line := range strings.Split(output, "\n") {
		candidate := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(line), "-", ""))
		if candidate == normalised {
			return true
		}
	}
	return false
}
