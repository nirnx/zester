package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
)

// UseraddProvider implements UserExec for Linux systems using
// useradd/usermod/userdel commands.
type UseraddProvider struct {
	cmd CommandExec
}

// NewUseraddProvider creates a UseraddProvider with the given CommandExec.
func NewUseraddProvider(cmd CommandExec) *UseraddProvider {
	return &UseraddProvider{cmd: cmd}
}

func (p *UseraddProvider) Lookup(_ context.Context, name string) (*UserInfo, error) {
	u, err := user.Lookup(name)
	if err != nil {
		var unknownUser user.UnknownUserError
		if errors.As(err, &unknownUser) {
			return nil, nil
		}
		return nil, fmt.Errorf("exec: lookup user %s: %w", name, err)
	}

	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)

	info := &UserInfo{
		Name:     u.Username,
		Home:     u.HomeDir,
		FullName: u.Name,
		UID:      uid,
		GID:      gid,
	}

	// Resolve supplementary group names.
	groupIDs, err := u.GroupIds()
	if err == nil {
		for _, gidStr := range groupIDs {
			g, err := user.LookupGroupId(gidStr)
			if err == nil {
				info.Groups = append(info.Groups, g.Name)
			}
		}
	}

	// Resolve login shell from /etc/passwd via getent.
	// os/user doesn't expose the shell field.
	info.Shell = lookupShell(name)

	return info, nil
}

// lookupShell reads the user's login shell from /etc/passwd.
// The os/user package parses /etc/passwd but doesn't expose the shell field.
func lookupShell(name string) string {
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	prefix := name + ":"
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			fields := strings.Split(line, ":")
			if len(fields) >= 7 {
				return fields[6]
			}
		}
	}
	return ""
}

func (p *UseraddProvider) Create(ctx context.Context, opts UserCreateOpts) error {
	args := make([]string, 0, 16)

	if opts.UID != 0 {
		args = append(args, "-u", strconv.Itoa(opts.UID))
	}
	if opts.PrimaryGroup != "" {
		args = append(args, "-g", opts.PrimaryGroup)
	} else if opts.GID != 0 {
		args = append(args, "-g", strconv.Itoa(opts.GID))
	}
	if len(opts.Groups) > 0 {
		args = append(args, "-G", strings.Join(opts.Groups, ","))
	}
	if opts.Home != "" {
		args = append(args, "-d", opts.Home)
	}
	if opts.Shell != "" {
		args = append(args, "-s", opts.Shell)
	}
	if opts.CreateHome {
		args = append(args, "-m")
	}
	if opts.System {
		args = append(args, "-r")
	}
	if opts.Password != "" {
		args = append(args, "-p", opts.Password)
	}
	if opts.FullName != "" {
		args = append(args, "-c", opts.FullName)
	}

	args = append(args, opts.Name)

	_, err := p.cmd.Run(ctx, CommandOpts{
		Command: "useradd",
		Args:    args,
	})
	if err != nil {
		return fmt.Errorf("exec: useradd %s: %w", opts.Name, err)
	}
	return nil
}

func (p *UseraddProvider) Modify(ctx context.Context, name string, opts UserModifyOpts) error {
	args := make([]string, 0, 12)

	if opts.UID != nil {
		args = append(args, "-u", strconv.Itoa(*opts.UID))
	}
	if opts.GID != nil {
		args = append(args, "-g", strconv.Itoa(*opts.GID))
	}
	if opts.Groups != nil {
		args = append(args, "-G", strings.Join(*opts.Groups, ","))
	}
	if opts.Home != nil {
		args = append(args, "-d", *opts.Home)
	}
	if opts.Shell != nil {
		args = append(args, "-s", *opts.Shell)
	}
	if opts.Password != nil {
		args = append(args, "-p", *opts.Password)
	}
	if opts.FullName != nil {
		args = append(args, "-c", *opts.FullName)
	}

	if len(args) == 0 {
		return nil // nothing to modify
	}

	args = append(args, name)

	_, err := p.cmd.Run(ctx, CommandOpts{
		Command: "usermod",
		Args:    args,
	})
	if err != nil {
		return fmt.Errorf("exec: usermod %s: %w", name, err)
	}
	return nil
}

func (p *UseraddProvider) Delete(ctx context.Context, name string, removeHome bool) error {
	args := make([]string, 0, 2)
	if removeHome {
		args = append(args, "-r")
	}
	args = append(args, name)

	_, err := p.cmd.Run(ctx, CommandOpts{
		Command: "userdel",
		Args:    args,
	})
	if err != nil {
		return fmt.Errorf("exec: userdel %s: %w", name, err)
	}
	return nil
}
