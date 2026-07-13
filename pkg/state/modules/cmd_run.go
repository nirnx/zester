package modules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/paramtypes"
	"github.com/nirnx/zester/pkg/state"
)

// CmdRun implements the cmd.run state.
// It executes a command on the target system and captures its output.
//
// CmdRun is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module). `command`
// is the primary parameter (defaults to the state ID) and accepts the `name`
// alias, so the Salt idiom `cmd.run: - name: <command>` runs the named command
// (command wins if both are set — BD-8, where legacy silently ran the state ID);
// `args` is a
// paramtypes.StringList and `env` a paramtypes.StringMap (so a scalar list/map
// value is rendered to a string rather than silently dropped, and a nested
// element is a typed error — BD-5, the same class as cmd.run's execmod sibling
// and file.append's text). The other string params (`cwd`, `creates`) and the
// primary coerce a numeric scalar to its string form (and reject a composite —
// BD-6) where the legacy `.(string)` assertion silently zeroed them. The
// require-file-provider-when-creates rule stays in the builder tail (it is
// cross-field module logic, not schema). The unexported runtime fields are
// untagged, so the schema compiler skips them.
type CmdRun struct {
	id   string
	reqs state.Requisites

	// Command is the command to execute; it defaults to the state ID. It accepts
	// the `name` alias so the Salt idiom `cmd.run: {name: <command>}` runs the
	// named command (command wins if both are set — see BD-8), and the `cmd`
	// alias so the execution-module spelling (`zester '<target>' cmd.run
	// cmd=uptime`) works on the state-dispatch path the CLI actually takes
	// (review round 5: the operator CLI parsed cmd= but strict decode then
	// rejected it as unknown).
	Command string `zester:"command,primary,aliases=name|cmd" usage:"command to execute; the name and cmd aliases are accepted (Salt state and execution-module spellings); defaults to the state ID"`

	// Args are additional arguments passed to the command. When empty the command
	// is run via the shell (sh -c); when non-empty it is executed directly with
	// these arguments.
	Args paramtypes.StringList `zester:"args" usage:"additional arguments passed to the command; when empty the command runs via the shell (sh -c), when non-empty it is executed directly with these arguments"`

	// Cwd is the working directory for the command. It accepts the `dir` alias
	// — the execution-module spelling (`salt['cmd.run'](cmd='ls', dir='/tmp')`)
	// — so the same key works on the state-dispatch path the CLI takes (round
	// 5: the same class as the `cmd` alias on the primary).
	Cwd string `zester:"cwd,aliases=dir" usage:"working directory for the command; the dir alias (execution-module spelling) is accepted; defaults to the peel process's working directory"`

	// Env is a map of environment variables to set for the command; they are
	// merged with the peel process's environment.
	Env paramtypes.StringMap `zester:"env" usage:"environment variables to set for the command; merged with the peel process's environment"`

	// Creates is a path that, if it exists, means the command has already run.
	// This provides idempotency: skip execution when the creates path exists.
	// The guard is evaluated independently in BOTH Check and Apply — a
	// watch-forced apply bypasses Check, and must still honor creates.
	Creates string `zester:"creates" usage:"a file path that, if it exists, means the command has already run — provides idempotency by skipping execution when the path exists"`

	// cmd is the injected command execution provider.
	cmd exec.CommandExec

	// file is the injected file execution provider (used for Creates check).
	file exec.FileExec

	// output stores the command output for reporting.
	output string
}

// cmdRunSpec is the compiled schema + documentation for cmd.run. Its Doc is
// drift-corrected against the live Check/Apply/Revert behavior — notably the
// creates guard gating BOTH phases, the shell-vs-direct execution split on args,
// and the non-revertible Revert.
var cmdRunSpec = mustSpec("cmd.run", modschema.KindState, CmdRun{}, modschema.Doc{
	Summary: "Execute a command on the target system and capture its output.",
	Description: "`cmd.run` executes a command (`command`, defaulting to the state ID) on the target and " +
		"captures its output. For Salt compatibility `command` also accepts the `name` alias, so " +
		"`cmd.run: - name: apt-get update` runs `apt-get update` (a declared `command` wins over `name`). " +
		"Commands are not inherently idempotent, so the `creates` parameter provides " +
		"an idempotency mechanism: a file path that, when it already exists, means the command has already " +
		"run and is skipped.\n\n" +
		"When `args` is empty the command string is run through the shell (`sh -c`), so shell features " +
		"(pipes, redirection, `&&`) work; when `args` is non-empty the command is executed directly with " +
		"those arguments (no shell). `cwd` sets the working directory and `env` sets environment variables " +
		"(merged with the peel process's environment).\n\n" +
		"`cmd.run` is ALSO a remote-execution module of the same name: the state module documented here is " +
		"the primary surface, and the execmod sibling is reachable from templates via `salt['cmd.run'](...)` " +
		"and from the CLI as an ad-hoc `zester '<target>' cmd.run '<command>'`.",
	Effects: modschema.Effects{
		Check: "When `creates` is set and the path already exists, reports no change (the command is " +
			"considered already run); a stat error other than not-exist fails the phase rather than " +
			"re-running. Otherwise always reports a change — the command needs to run.",
		Apply: "Re-evaluates the `creates` guard independently of Check (a watch-forced Apply bypasses " +
			"Check, and a creates-guarded one-shot must not re-run just because a watched dependency " +
			"changed): when the path exists, the command is not run and Apply is a no-op. Otherwise it runs " +
			"the command — through the shell (`sh -c`) when `args` is empty, directly with the arguments " +
			"otherwise — in `cwd` with `env` merged into the process environment, capturing stdout, stderr, " +
			"and the exit code into its details. A non-zero exit is returned as an error (with the captured " +
			"output still in details).",
		Revert: "Commands are not revertible: Revert is a no-op (`Changed: false`). Manage any undo logic " +
			"with a separate state.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Run a command ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the command string.",
			Code:        "zester 'web*' cmd.run 'df -h'",
		},
		{
			Title:       "One-shot command guarded by creates",
			Kind:        "state",
			Explanation: "creates makes the command idempotent — it is skipped once the marker path exists.",
			Code: "initialize_database:\n  cmd.run:\n    - command: /usr/local/bin/init-db --setup\n" +
				"    - creates: /var/lib/myapp/.db-initialized\n",
		},
		{
			Title:       "Command with arguments and a working directory",
			Kind:        "state",
			Explanation: "A non-empty args list runs the command directly (no shell) in cwd.",
			Code: "build_application:\n  cmd.run:\n    - command: make\n    - args:\n" +
				"      - build\n      - \"-j4\"\n    - cwd: /opt/myapp/src\n",
		},
		{
			Title:       "Command with environment variables after a dependency",
			Kind:        "state",
			Explanation: "env is merged with the process environment; require orders this after the package install.",
			Code: "run_migrations:\n  cmd.run:\n    - command: python manage.py migrate --noinput\n" +
				"    - cwd: /opt/webapp\n    - env:\n        DJANGO_SETTINGS_MODULE: myproject.settings.production\n" +
				"    - creates: /opt/webapp/.migrations-done\n    - require:\n      - \"pkg.installed:python3\"\n",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "creates provides idempotency and guards both phases",
			Body: "A command is not inherently idempotent. Set `creates` to a path the command produces: " +
				"once it exists, both Check and Apply skip the command (a watch-forced Apply honors it too). A " +
				"stat error other than \"not found\" fails the phase rather than falling through to re-running " +
				"a destructive one-shot.",
		},
		{
			Level: "info",
			Title: "Shell vs. direct execution depends on args",
			Body: "With no `args`, the command string is run through `sh -c`, so pipes, redirection, and " +
				"`&&` work. With a non-empty `args` list the command is executed directly with those " +
				"arguments and no shell. Under the uniform decoder a bare-string `args` value (or a CLI " +
				"`args=...`) is a one-element list — which switches execution to the direct (non-shell) path — " +
				"where the legacy list assertion silently dropped it and left the shell path in effect (BD-5).",
		},
		{
			Level: "info",
			Title: "Also a remote-execution module",
			Body: "`cmd.run` is also an execution module of the same name — reachable from templates via " +
				"`salt['cmd.run'](...)` and from the CLI as `zester '<target>' cmd.run '<command>'`. The state " +
				"module documented here (with `creates` idempotency and the Check/Apply/Revert lifecycle) is " +
				"the primary surface.",
		},
		{
			Level: "info",
			Title: "The name and cmd aliases run the command (Salt idioms)",
			Body: "For Salt compatibility the primary `command` accepts the `name` alias: `cmd.run: - name: " +
				"apt-get update` runs `apt-get update`. Zester previously read only `command` and silently ran the " +
				"STATE ID when only `name:` was given (quietly wrong); it now executes the named command, matching " +
				"Salt (BD-8). The execution-module spelling `cmd` is also accepted (so `zester '<target>' cmd.run " +
				"cmd=uptime` works even though the CLI dispatches through this state module). Source resolution " +
				"order is `command` > `name` > `cmd`; an empty-string value falls through to the next source " +
				"before the state-ID fallback.",
		},
		{
			Level: "info",
			Title: "Details returned",
			Body: "After execution the result details carry `command` (the command run), `stdout` and " +
				"`stderr` (captured output), and `exitcode` (the process exit code as a string).",
		},
	},
	Divergences: []string{"BD-5", "BD-6", "BD-8"},
	SeeAlso:     []string{"module.run"},
})

// NewCmdRunBuilder returns a state.Builder that creates CmdRun states using the
// given ModuleContext's command and file providers. Decode policy (unknown-key
// handling, reserved keys) is threaded via opts; the peel supplies it through
// modules.RegisterAll.
func NewCmdRunBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected providers, id, and
		// requisites MUST be assigned AFTER it.
		c := &CmdRun{}
		if _, err := cmdRunSpec.Decode(id, config, c, opts); err != nil {
			return nil, fmt.Errorf("cmd.run: %w", err)
		}
		c.id = id
		c.cmd = mctx.Command
		c.file = mctx.File
		c.reqs = state.ParseRequisites(config)

		// Cross-field module logic (not schema): the creates guard reads the path
		// via the file provider, so a nil file provider is only fatal when creates
		// is declared.
		if c.Creates != "" && c.file == nil {
			return nil, fmt.Errorf("cmd.run: %s: creates requires a file provider", id)
		}

		return c, nil
	}
}

func (c *CmdRun) Name() string           { return "cmd.run:" + c.id }
func (c *CmdRun) Reqs() state.Requisites { return c.reqs }

// createsSatisfied reports whether the creates guard path exists. Only
// fs.ErrNotExist counts as absent; any other stat error fails the phase —
// creates canonically guards destructive one-shots (initdb, mkfs), so an
// unverifiable guard must never fall through to re-execution.
func (c *CmdRun) createsSatisfied(ctx context.Context) (bool, error) {
	if c.Creates == "" {
		return false, nil
	}
	if _, err := c.file.Stat(ctx, c.Creates); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("cmd.run: stat creates path %s: %w", c.Creates, err)
	}
	return true, nil
}

func (c *CmdRun) Check(ctx context.Context) (state.CheckResult, error) {
	satisfied, err := c.createsSatisfied(ctx)
	if err != nil {
		return state.CheckResult{}, err
	}
	if satisfied {
		return state.CheckResult{
			NeedsChange: false,
			Diff:        fmt.Sprintf("creates path %s already exists", c.Creates),
		}, nil
	}

	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("command %q will run", c.Command),
	}, nil
}

func (c *CmdRun) Apply(ctx context.Context) (state.ApplyResult, error) {
	// The creates guard gates Apply too, independently of Check: watch-forced
	// applies bypass Check entirely, and a creates-guarded one-shot must not
	// re-run just because a watched dependency changed (Salt honors creates
	// on watch-triggered runs via mod_run_check).
	satisfied, err := c.createsSatisfied(ctx)
	if err != nil {
		return state.ApplyResult{}, err
	}
	if satisfied {
		return state.ApplyResult{
			Changed: false,
			Diff:    fmt.Sprintf("creates path %s already exists; command not run", c.Creates),
		}, nil
	}

	opts := exec.CommandOpts{
		Command: c.Command,
		Args:    c.Args,
		Shell:   len(c.Args) == 0,
		Dir:     c.Cwd,
		Env:     c.Env,
	}

	result, err := c.cmd.Run(ctx, opts)

	details := map[string]string{
		"command":  c.Command,
		"stdout":   "",
		"stderr":   "",
		"exitcode": "-1",
	}

	if result != nil {
		c.output = result.Stdout
		details["stdout"] = result.Stdout
		details["stderr"] = result.Stderr
		details["exitcode"] = fmt.Sprintf("%d", result.ExitCode)
	}

	if err != nil {
		return state.ApplyResult{
			Changed: true,
			Details: details,
		}, fmt.Errorf("cmd.run: %s: %w", c.Command, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("executed %q (exit 0)", c.Command),
		Details: details,
	}, nil
}

func (c *CmdRun) Revert(_ context.Context) (state.ApplyResult, error) {
	return state.ApplyResult{
		Changed: false,
		Diff:    "cmd.run states cannot be reverted",
	}, nil
}
