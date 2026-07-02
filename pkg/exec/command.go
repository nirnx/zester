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

func (e *OSCommandExec) Run(ctx context.Context, opts CommandOpts) (*CommandResult, error) {
	var cmd *osexec.Cmd

	if len(opts.Args) == 0 && opts.Shell {
		cmd = osexec.CommandContext(ctx, "sh", "-c", opts.Command)
	} else if len(opts.Args) == 0 {
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
