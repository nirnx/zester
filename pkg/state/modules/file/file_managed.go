package filemod

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"strconv"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/paramtypes"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
)

// FileManaged implements the file.managed state.
// It ensures a file exists with the specified content, permissions, and ownership.
//
// FileManaged is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module). Two of
// them are named semantic types: Template is a paramtypes.TemplateFlag (a bool,
// the string "jinja", or any truthy/falsy string — BD-2, so a CLI `template=true`
// now enables rendering where the legacy `== "jinja"` check silently ignored it)
// and Mode is a paramtypes.FileMode declared `lazy,default=0644` — the mode is
// never materialized into the struct, the module applies the documented 0644
// default at use time via Mode.Resolve(0644). FileMode honors an octal-int mode
// of ANY integer kind, so a reactor-dispatched `mode: 0755` (delivered as a
// msgpack-sized uint16) now applies 0755 rather than silently dropping to 0644
// (BD-1). The unexported runtime fields (id, reqs, injected providers, revert
// memos) are untagged, so the schema compiler skips them.
type FileManaged struct {
	id   string
	reqs state.Requisites

	// Path is the absolute path to the file; it defaults to the state ID.
	Path string `zester:"name,primary,aliases=path" usage:"absolute path to the target file; the path alias is accepted for Salt compatibility; defaults to the state ID"`
	// Content is the desired inline file content. Provide either content or
	// source; if both are set, source wins.
	Content string `zester:"content" usage:"desired inline file content; provide either content or source (source wins if both are set)"`
	// Source: file.* family component (source; requiredness member-supplied —
	// optional here, content is the alternative).
	fileSourceParam
	// Template selects Jinja2 rendering of the source/content before writing.
	Template paramtypes.TemplateFlag `zester:"template" usage:"render source/content as a Jinja2 template before writing (a bool, the string \"jinja\", or a truthy/falsy string); defaults to off"`
	// Context provides extra template variables (override Defaults).
	Context map[string]any `zester:"context" usage:"extra template variables, available as top-level names; override defaults; only used when template is enabled"`
	// Defaults provides fallback template variables (Context takes precedence).
	Defaults map[string]any `zester:"defaults" usage:"fallback template variables; context takes precedence; only used when template is enabled"`
	// Mode: file.* family component (member-supplied default 0644, see mustSpec).
	fileModeParam
	// User/Group: file.* family ownership component.
	fileOwnershipParam
	// MakeDirs: file.* family component (canonical parent-creation contract).
	fileMakeDirsParam

	// file is the injected file execution provider.
	file exec.FileExec

	// render is the injected template rendering function.
	render func(name, source string, extra map[string]any) (string, error)

	// backup stores original content (and its mode) for revert.
	backup     []byte
	backupMode fs.FileMode
	backupSet  bool

	// wasCreated records that Apply created the file (it did not pre-exist),
	// so a same-instance Revert removes it. With neither memo set, Revert is
	// a clean no-op.
	wasCreated bool
}

// fileManagedSpec is the compiled schema + documentation for file.managed. It is
// compiled once at package init and executed by every decode path (the builder
// below, and Registry.Parse). The prose is drift-corrected against the live
// Check/Apply/Revert behavior (notably: content/source are alternatives with no
// parse-time exclusion — source wins when both are set — and the mode facet is
// enforced with a Chmod on pre-existing files, not only at creation).
var fileManagedSpec = regdef.MustSpec("file.managed", modschema.KindState, FileManaged{}, fileManagedDoc,
	modschema.WithDefault("mode", "0644"), modschema.WithRequired("source", false))

var fileManagedDoc = modschema.Doc{
	Summary: "Ensure a file exists with the desired content, permissions, and ownership.",
	Description: "`file.managed` ensures a file exists at its path with the desired content, " +
		"permission mode, and ownership. The path defaults to the state ID (the `path` alias is " +
		"accepted for Salt compatibility). Supply the content inline with `content` or copy it from a " +
		"local file with `source` — these are alternatives, and if both are given `source` wins " +
		"(there is no parse-time mutual-exclusion check). Setting `template` renders the content or " +
		"source through the Jinja2 engine before writing (opt-in, so files that legitimately contain " +
		"`{{ }}` are left untouched by default). The `mode` parameter defaults to `0644` and honors " +
		"an octal string or an octal integer of any kind, so a reactor-dispatched `mode: 0755` " +
		"applies 0755. `user`/`group` converge ownership only when declared.",
	Effects: modschema.Effects{
		Check: "Fails first when the target's parent directory is missing and `makedirs` is unset (the canonical file.* contract). " +
			"Resolves the desired content (reading `source` or using `content`, rendering the " +
			"template when enabled) and compares, in order: file existence (a genuine not-exist means " +
			"the file must be created, while any other read error fails the check rather than risking a " +
			"blind overwrite); the SHA-256 content hash; the permission mode (comparing the managed " +
			"facets — permission bits plus setuid/setgid/sticky); and, only when `user`/`group` are " +
			"declared, ownership drift. Reports a change on the first mismatch.",
		Apply: "Probes the prior state for revert (a non-not-exist read error fails the apply rather " +
			"than overwriting content it could not capture), resolves the desired content and mode, " +
			"creates missing parent directories with mode 0755 when `makedirs` is set, then writes the " +
			"content. It records the revert memo on the first write only (first-capture-wins; a file it " +
			"created stays marked created). Ownership is applied BEFORE the mode — chown clears the " +
			"setuid/setgid bits, so the mode is chmod'd last — and the mode is enforced on pre-existing " +
			"files too (os.WriteFile only applies perms at creation), so a mode-only drift converges. " +
			"Reports the byte count and the applied mode.",
		Revert: "Restores what this run's Apply changed: a file that pre-existed is rewritten with its " +
			"captured prior content AND prior mode (never the mode Apply set); a file this instance " +
			"created is removed. A fresh instance (a standalone revert) recorded nothing and is an " +
			"explicit clean no-op — it never destroys a file it did not touch.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Write a file with inline content",
			Kind:        "state",
			Explanation: "The state ID is the target path; content and mode are set inline.",
			Code:        "/etc/motd:\n  file.managed:\n    - content: \"Welcome to Zester\"\n    - mode: \"0644\"\n",
		},
		{
			Title:       "Copy from a source file with ownership",
			Kind:        "state",
			Explanation: "source copies a local file; makedirs creates any missing parent directories; user/group set ownership.",
			Code: "deploy-nginx-config:\n  file.managed:\n    - path: /etc/nginx/nginx.conf\n" +
				"    - source: /srv/zester/files/nginx.conf\n    - mode: \"0644\"\n    - user: root\n" +
				"    - group: root\n    - makedirs: true\n",
		},
		{
			Title:       "Create directories on demand",
			Kind:        "state",
			Explanation: "makedirs creates the whole parent chain (with mode 0755) before writing, so the state does not depend on a separate file.directory for /opt/myapp/config.",
			Code: "app_config:\n  file.managed:\n    - path: /opt/myapp/config/settings.yml\n" +
				"    - content: |\n        port: 8080\n        log_level: info\n" +
				"    - mode: \"0600\"\n    - user: myapp\n    - group: myapp\n    - makedirs: true\n",
		},
		{
			Title:       "Render a source template with variables",
			Kind:        "state",
			Explanation: "template: true renders the source through Jinja2; context overrides defaults (worker_count becomes 8).",
			Code: "/etc/nginx/nginx.conf:\n  file.managed:\n    - source: /srv/zester/files/nginx.conf.jinja\n" +
				"    - template: true\n    - defaults:\n        worker_count: 4\n    - context:\n        worker_count: 8\n" +
				"    - mode: \"0644\"\n",
		},
		{
			Title:       "Render inline content as a template",
			Kind:        "state",
			Explanation: "template: jinja renders the inline content; facts and settings are available under their own namespaces.",
			Code: "/etc/motd:\n  file.managed:\n    - template: jinja\n    - content: |\n" +
				"        Welcome to {{ facts.hostname }}.\n        Role: {{ settings.role }}\n    - mode: \"0644\"\n",
		},
		{
			Title:       "Write a file ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the path; content and mode are key=value args.",
			Code:        "zester 'web*' file.managed /etc/motd content=\"Welcome to Zester\" mode=0644",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Template rendering is opt-in and re-renders inline content",
			Body: "By default file.managed writes content verbatim. `template: true` (or `template: jinja`) " +
				"renders the source or inline content through Jinja2 before writing. Because `.zy` state " +
				"files are themselves rendered before YAML parsing, inline templated content is rendered a " +
				"second time at write; wrap it in `{% raw %}...{% endraw %}` or use a separate source file " +
				"to avoid the state-file render consuming your expressions.",
		},
		{
			Level: "info",
			Title: "Template variables and namespaces",
			Body: "When `template` is enabled, the rendered source or content can reference the target " +
				"peel's `facts.*` (every collected fact) and `settings.*` (every resolved setting), plus " +
				"any variables supplied under `context` and `defaults`, which are exposed as top-level " +
				"names. `context` overrides `defaults`; both are independent of the always-available " +
				"`facts` and `settings` namespaces.",
		},
		{
			Level: "info",
			Title: "Worked source-template render",
			Body: "Take the \"Render a source template with variables\" example above, whose " +
				"`nginx.conf.jinja` source contains:\n\n" +
				"```nginx\nworker_processes {{ worker_count }};\n" +
				"error_log /var/log/nginx/error.log {{ log_level }};\n```\n\n" +
				"With `defaults: {worker_count: 4}` and `context: {worker_count: 8}`, it renders " +
				"`worker_processes` as 8 — `context` overrides the default of 4 — while `facts` and " +
				"`settings` stay available under their own namespaces for any other expressions.",
		},
		{
			Title: "content and source are alternatives",
			Body: "Provide either content or source. There is no parse-time mutual-exclusion error; if both " +
				"are set, source takes precedence and content is ignored.",
		},
		{
			Title: "Mode defaults to 0644 and honors integer modes",
			Body: "An omitted mode applies 0644. A mode given as an octal integer (for example `mode: 0755`) " +
				"is honored across every delivery path, including a reactor dispatch where msgpack encodes it " +
				"as a sized integer; setuid/setgid/sticky bits are preserved.",
		},
	},
	Divergences: []string{"BD-1", "BD-2", "BD-6", "BD-7", "BD-9"},
	SeeAlso:     []string{"file.directory", "file.copy", "file.absent"},
}

// NewFileManagedBuilder returns a state.Builder that creates FileManaged states
// using the given ModuleContext's file and render providers. Decode policy
// (unknown-key handling, reserved keys) is threaded via opts; the peel supplies
// it through modules.RegisterAll.
func NewFileManagedBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file.managed: no file provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected providers, id, and
		// requisites MUST be assigned AFTER it — assigning them before would be
		// overwritten by the committed scratch value.
		f := &FileManaged{}
		if _, err := fileManagedSpec.Decode(id, config, f, opts); err != nil {
			return nil, fmt.Errorf("file.managed: %w", err)
		}
		f.id = id
		f.file = mctx.File
		f.render = mctx.RenderTemplate
		f.reqs = state.ParseRequisites(config)
		return f, nil
	}
}

func (f *FileManaged) Name() string           { return "file.managed:" + f.id }
func (f *FileManaged) Reqs() state.Requisites { return f.reqs }

func (f *FileManaged) desiredContent(ctx context.Context) ([]byte, error) {
	var raw string
	if f.Source != "" {
		data, err := f.file.ReadFile(ctx, f.Source)
		if err != nil {
			return nil, err
		}
		raw = string(data)
	} else {
		raw = f.Content
	}

	if f.Template.Enabled() && f.render != nil {
		extra := mergeDefaults(f.Defaults, f.Context)
		rendered, err := f.render(f.id, raw, extra)
		if err != nil {
			return nil, fmt.Errorf("render template: %w", err)
		}
		return []byte(rendered), nil
	}

	return []byte(raw), nil
}

// mergeDefaults merges defaults and context maps, with context taking precedence.
func mergeDefaults(defaults, context map[string]any) map[string]any {
	if defaults == nil && context == nil {
		return nil
	}
	merged := make(map[string]any)
	maps.Copy(merged, defaults)
	maps.Copy(merged, context)
	return merged
}

// desiredMode resolves the managed permission mode, supplying the documented
// 0644 default for the lazy, unmaterialized Mode field. FileMode parsed the
// value at decode time, so resolution here cannot fail.
func (f *FileManaged) desiredMode() fs.FileMode {
	return f.Mode.Resolve(0644)
}

func (f *FileManaged) Check(ctx context.Context) (state.CheckResult, error) {
	// Canonical makedirs contract (§13): a missing parent with makedirs unset
	// fails Check too — never reported as an applicable change.
	if err := requireParentDirs(ctx, f.file, "file.managed", f.Path, f.MakeDirs); err != nil {
		return state.CheckResult{}, err
	}
	desired, err := f.desiredContent(ctx)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("file.managed: %w", err)
	}

	current, err := f.file.ReadFile(ctx, f.Path)
	if err != nil {
		// Only a genuine not-exist means "file absent"; any other read error
		// (permissions, I/O) fails the check rather than risking a blind
		// overwrite in Apply.
		if !errors.Is(err, fs.ErrNotExist) {
			return state.CheckResult{}, fmt.Errorf("file.managed: read %s: %w", f.Path, err)
		}
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("file %s does not exist", f.Path),
		}, nil
	}

	if hashBytes(current) != hashBytes(desired) {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("content differs for %s", f.Path),
		}, nil
	}

	mode := f.desiredMode()
	info, err := f.file.Stat(ctx, f.Path)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("file.managed: stat %s: %w", f.Path, err)
	}
	if !modesEqual(info.Mode(), mode) {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("mode %s != %s for %s", octalMode(info.Mode()), octalMode(mode), f.Path),
		}, nil
	}

	diff, drift, err := checkOwnershipDrift(ctx, f.file, "file.managed", f.Path, f.User, f.Group)
	if err != nil {
		return state.CheckResult{}, err
	}
	if drift {
		return state.CheckResult{NeedsChange: true, Diff: diff}, nil
	}

	return state.CheckResult{NeedsChange: false}, nil
}

func (f *FileManaged) Apply(ctx context.Context) (state.ApplyResult, error) {
	// Probe prior state for revert. Only a genuine not-exist means "will
	// create"; any other read error fails the apply rather than overwriting
	// a file whose current content could not be captured.
	preExisted := false
	var priorContent []byte
	var priorMode fs.FileMode
	existing, err := f.file.ReadFile(ctx, f.Path)
	switch {
	case err == nil:
		preExisted = true
		priorContent = existing
		// Capture the prior mode too, so Revert can restore it (not the
		// desired mode, which is what Apply sets).
		info, statErr := f.file.Stat(ctx, f.Path)
		if statErr != nil {
			return state.ApplyResult{}, fmt.Errorf("file.managed: stat %s: %w", f.Path, statErr)
		}
		priorMode = info.Mode().Perm()
	case errors.Is(err, fs.ErrNotExist):
		// Will create.
	default:
		return state.ApplyResult{}, fmt.Errorf("file.managed: read %s: %w", f.Path, err)
	}

	desired, err := f.desiredContent(ctx)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.managed: %w", err)
	}

	mode := f.desiredMode()

	if err := ensureParentDirs(ctx, f.file, "file.managed", f.Path, f.MakeDirs); err != nil {
		return state.ApplyResult{}, err
	}

	if err := f.file.WriteFile(ctx, f.Path, desired, mode); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.managed: write %s: %w", f.Path, err)
	}

	// Record revert memos only after the write actually changed the system.
	// First capture wins: a re-Apply on the same instance (retry:, watch-
	// forced runs) must not clobber the original backup with already-applied
	// content — and a file this instance CREATED stays wasCreated, or Revert
	// would rewrite content into a file that never pre-existed instead of
	// removing it.
	if preExisted {
		if !f.backupSet && !f.wasCreated {
			f.backup = priorContent
			f.backupMode = priorMode
			f.backupSet = true
		}
	} else {
		f.wasCreated = true
	}

	// Ownership BEFORE mode: chown on an executable clears setuid/setgid
	// (kernel behavior), so chmod must run last or a declared "4755" +
	// user:/group: would lose the setuid bit every apply.
	if err := f.setOwnership(ctx); err != nil {
		return state.ApplyResult{}, err
	}

	// os.WriteFile applies perm at creation only — enforce the desired mode
	// on pre-existing files too, or the mode drift Check reports would never
	// converge (Apply would rewrite identical bytes forever).
	if err := f.file.Chmod(ctx, f.Path, mode); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.managed: chmod %s: %w", f.Path, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("wrote %d bytes to %s (mode %o)", len(desired), f.Path, mode),
		Details: map[string]string{
			"path":  f.Path,
			"bytes": strconv.Itoa(len(desired)),
			"mode":  fmt.Sprintf("%04o", mode),
		},
	}, nil
}

func (f *FileManaged) Revert(ctx context.Context) (state.ApplyResult, error) {
	switch {
	case f.backupSet:
		if err := f.file.WriteFile(ctx, f.Path, f.backup, f.backupMode); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.managed: revert %s: %w", f.Path, err)
		}
		// os.WriteFile sets perm at creation only; restore the CAPTURED
		// prior mode explicitly (never the desired mode — that is what
		// Apply set).
		if err := f.file.Chmod(ctx, f.Path, f.backupMode); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.managed: revert chmod %s: %w", f.Path, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("reverted %s to previous content", f.Path),
		}, nil

	case f.wasCreated:
		if err := f.file.Remove(ctx, f.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return state.ApplyResult{}, fmt.Errorf("file.managed: remove %s: %w", f.Path, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("removed %s (revert create)", f.Path),
		}, nil

	default:
		// No apply recorded on this instance: never destroy a file we did
		// not touch.
		return state.ApplyResult{
			Changed: false,
			Diff:    "nothing to revert (no apply recorded in this run)",
		}, nil
	}
}

func (f *FileManaged) setOwnership(ctx context.Context) error {
	if f.User == "" && f.Group == "" {
		return nil
	}

	uid, gid, err := resolveOwnerIDs("file.managed", f.User, f.Group)
	if err != nil {
		return err
	}

	if err := f.file.Chown(ctx, f.Path, uid, gid); err != nil {
		return fmt.Errorf("file.managed: chown %s: %w", f.Path, err)
	}
	return nil
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return 0
}
