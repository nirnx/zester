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

// GroupaddProvider implements GroupExec for Linux systems using
// groupadd/groupmod/groupdel/gpasswd commands.
type GroupaddProvider struct {
	cmd CommandExec
}

// NewGroupaddProvider creates a GroupaddProvider with the given CommandExec.
func NewGroupaddProvider(cmd CommandExec) *GroupaddProvider {
	return &GroupaddProvider{cmd: cmd}
}

func (p *GroupaddProvider) Lookup(_ context.Context, name string) (*GroupInfo, error) {
	g, err := user.LookupGroup(name)
	if err != nil {
		var unknownGroup user.UnknownGroupError
		if errors.As(err, &unknownGroup) {
			return nil, nil
		}
		return nil, fmt.Errorf("exec: lookup group %s: %w", name, err)
	}

	gid, _ := strconv.Atoi(g.Gid)

	info := &GroupInfo{
		Name: g.Name,
		GID:  gid,
	}

	// os/user doesn't expose group members, so parse /etc/group.
	info.Members = parseGroupMembers(name)

	return info, nil
}

// parseGroupMembers reads the member list for a group from /etc/group.
func parseGroupMembers(name string) []string {
	data, err := os.ReadFile("/etc/group")
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 4 && fields[0] == name {
			members := strings.TrimSpace(fields[3])
			if members == "" {
				return nil
			}
			return strings.Split(members, ",")
		}
	}
	return nil
}

func (p *GroupaddProvider) Create(ctx context.Context, opts GroupCreateOpts) error {
	args := make([]string, 0, 4)

	if opts.GID != 0 {
		args = append(args, "-g", strconv.Itoa(opts.GID))
	}
	if opts.System {
		args = append(args, "-r")
	}

	args = append(args, opts.Name)

	_, err := p.cmd.Run(ctx, CommandOpts{
		Command: "groupadd",
		Args:    args,
	})
	if err != nil {
		return fmt.Errorf("exec: groupadd %s: %w", opts.Name, err)
	}
	return nil
}

func (p *GroupaddProvider) Modify(ctx context.Context, name string, opts GroupModifyOpts) error {
	// Change GID if requested.
	if opts.GID != nil {
		_, err := p.cmd.Run(ctx, CommandOpts{
			Command: "groupmod",
			Args:    []string{"-g", strconv.Itoa(*opts.GID), name},
		})
		if err != nil {
			return fmt.Errorf("exec: groupmod %s: %w", name, err)
		}
	}

	// Add members via gpasswd.
	for _, member := range opts.AddMembers {
		_, err := p.cmd.Run(ctx, CommandOpts{
			Command: "gpasswd",
			Args:    []string{"-a", member, name},
		})
		if err != nil {
			return fmt.Errorf("exec: gpasswd -a %s %s: %w", member, name, err)
		}
	}

	// Remove members via gpasswd.
	for _, member := range opts.DelMembers {
		_, err := p.cmd.Run(ctx, CommandOpts{
			Command: "gpasswd",
			Args:    []string{"-d", member, name},
		})
		if err != nil {
			return fmt.Errorf("exec: gpasswd -d %s %s: %w", member, name, err)
		}
	}

	return nil
}

func (p *GroupaddProvider) Delete(ctx context.Context, name string) error {
	_, err := p.cmd.Run(ctx, CommandOpts{
		Command: "groupdel",
		Args:    []string{name},
	})
	if err != nil {
		return fmt.Errorf("exec: groupdel %s: %w", name, err)
	}
	return nil
}
