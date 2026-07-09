package exec

import (
	"context"
	"fmt"
)

// AptProvider implements PackageExec for Debian/Ubuntu systems using apt-get.
type AptProvider struct {
	cmd CommandExec
}

func NewAptProvider(cmd CommandExec) *AptProvider {
	return &AptProvider{cmd: cmd}
}

func (a *AptProvider) Name() string { return "apt" }

// aptEnv forces apt-get fully non-interactive: no debconf prompts. Without it a
// prompt blocks forever on the peel's serialized exec worker, wedging every
// mutating job behind it (there is no TTY to answer).
func aptEnv() map[string]string {
	return map[string]string{"DEBIAN_FRONTEND": "noninteractive"}
}

// dpkgConfArgs resolve conffile conflicts without prompting: keep the admin's
// modified conffile (--force-confold), use the package default where there is
// no local edit (--force-confdef). A modified conffile (e.g. an operator-edited
// /etc/zester/peel.yaml) would otherwise prompt and hang the exec worker.
var dpkgConfArgs = []string{
	"-o", "Dpkg::Options::=--force-confdef",
	"-o", "Dpkg::Options::=--force-confold",
}

func (a *AptProvider) IsInstalled(ctx context.Context, pkg string) (bool, error) {
	_, err := a.cmd.Run(ctx, CommandOpts{
		Command: "dpkg",
		Args:    []string{"-s", pkg},
	})
	if err != nil {
		return false, nil
	}
	return true, nil
}

func (a *AptProvider) Install(ctx context.Context, pkg string, version string) error {
	target := pkg
	if version != "" {
		target = pkg + "=" + version
	}
	_, err := a.cmd.Run(ctx, CommandOpts{
		Command: "apt-get",
		Args:    append(append([]string{"install", "-y"}, dpkgConfArgs...), target),
		Env:     aptEnv(),
	})
	if err != nil {
		return fmt.Errorf("apt install %s: %w", target, err)
	}
	return nil
}

func (a *AptProvider) Remove(ctx context.Context, pkg string) error {
	_, err := a.cmd.Run(ctx, CommandOpts{
		Command: "apt-get",
		Args:    append(append([]string{"remove", "-y"}, dpkgConfArgs...), pkg),
		Env:     aptEnv(),
	})
	if err != nil {
		return fmt.Errorf("apt remove %s: %w", pkg, err)
	}
	return nil
}

func (a *AptProvider) Refresh(ctx context.Context) error {
	_, err := a.cmd.Run(ctx, CommandOpts{
		Command: "apt-get",
		Args:    []string{"update"},
		Env:     aptEnv(),
	})
	if err != nil {
		return fmt.Errorf("apt update: %w", err)
	}
	return nil
}
