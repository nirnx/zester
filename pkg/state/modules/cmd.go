package modules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// CmdRun implements the cmd.run state.
// It executes a command on the target system and captures its output.
type CmdRun struct {
	id   string
	reqs state.Requisites

	// Command is the command to execute.
	Command string

	// Args are additional arguments passed to the command.
	Args []string

	// Cwd is the working directory for the command.
	Cwd string

	// Env is a map of environment variables to set.
	Env map[string]string

	// Creates is a path that, if it exists, means the command has already run.
	// This provides idempotency: skip execution when the creates path exists.
	// The guard is evaluated independently in BOTH Check and Apply — a
	// watch-forced apply bypasses Check, and must still honor creates.
	Creates string

	// cmd is the injected command execution provider.
	cmd exec.CommandExec

	// file is the injected file execution provider (used for Creates check).
	file exec.FileExec

	// output stores the command output for reporting.
	output string
}

// NewCmdRunBuilder returns a state.Builder that creates CmdRun states
// using the given ModuleContext's command and file providers.
func NewCmdRunBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		return newCmdRun(id, config, mctx.Command, mctx.File)
	}
}

func newCmdRun(id string, config map[string]any, cmd exec.CommandExec, file exec.FileExec) (state.State, error) {
	c := &CmdRun{id: id, cmd: cmd, file: file}

	c.Command, _ = config["command"].(string)
	if c.Command == "" {
		c.Command = id
	}

	if args, ok := config["args"].([]any); ok {
		for _, a := range args {
			if s, ok := a.(string); ok {
				c.Args = append(c.Args, s)
			}
		}
	}

	c.Cwd, _ = config["cwd"].(string)
	c.Creates, _ = config["creates"].(string)
	if c.Creates != "" && file == nil {
		return nil, fmt.Errorf("cmd.run: %s: creates requires a file provider", id)
	}

	if env, ok := config["env"].(map[string]any); ok {
		c.Env = make(map[string]string, len(env))
		for k, v := range env {
			c.Env[k] = fmt.Sprintf("%v", v)
		}
	}

	c.reqs = state.ParseRequisites(config)

	return c, nil
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
