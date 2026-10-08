package exec

import (
	"bytes"
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"strings"
)

// OSCommandExec implements CommandExec using the os/exec package.
type OSCommandExec struct{}

// Run executes opts. The shell is involved ONLY when opts.Shell is true and
// no Args are given (`sh -c <Command>`); every other shape exec's Command
// directly as a binary name/path with Args passed verbatim. A Shell:false
// command string is therefore never word-split or interpreted — "echo hi"
// with no Args is a lookup failure for a binary literally named "echo hi",
// not a shell invocation — so a caller that forgot Shell:true cannot be
// turned into an injection sink by a value containing metacharacters.
func (e *OSCommandExec) Run(ctx context.Context, opts CommandOpts) (*CommandResult, error) {
	var cmd *osexec.Cmd
	if opts.Shell && len(opts.Args) == 0 {
		cmd = osexec.CommandContext(ctx, "sh", "-c", opts.Command)
	} else {
		cmd = osexec.CommandContext(ctx, opts.Command, opts.Args...)
	}

	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}

	if len(opts.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range opts.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	result := &CommandResult{
		Stdout:   strings.TrimSpace(stdout.String()),
		Stderr:   strings.TrimSpace(stderr.String()),
		ExitCode: exitCode,
	}

	if err != nil {
		return result, fmt.Errorf("exec: run %s: %w", opts.Command, err)
	}
	return result, nil
}
