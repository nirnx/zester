package modules

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// MountMounted implements the mount.mounted state.
// It ensures a filesystem is mounted and optionally present in fstab.
type MountMounted struct {
	id   string
	reqs state.Requisites

	MountPoint string
	Device     string
	FSType     string
	Options    string
	Dump       int
	Pass       int
	Persist    bool

	mount exec.MountExec

	// applied tracks what was done during Apply for Revert.
	appliedMount bool
	appliedFstab bool
}

// NewMountMountedBuilder returns a state.Builder that creates MountMounted states.
func NewMountMountedBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Mount == nil {
			return nil, fmt.Errorf("mount.mounted: no mount provider available")
		}
		return newMountMounted(id, config, mctx.Mount)
	}
}

func newMountMounted(id string, config map[string]any, mount exec.MountExec) (state.State, error) {
	m := &MountMounted{id: id, mount: mount}

	m.MountPoint, _ = config["name"].(string)
	if m.MountPoint == "" {
		m.MountPoint = id
	}

	m.Device, _ = config["device"].(string)
	if m.Device == "" {
		return nil, fmt.Errorf("mount.mounted: %s: device is required", id)
	}

	m.FSType, _ = config["fstype"].(string)
	if m.FSType == "" {
		m.FSType = "ext4"
	}

	m.Options, _ = config["opts"].(string)
	if m.Options == "" {
		m.Options = "defaults"
	}

	if v, ok := config["dump"].(int); ok {
		m.Dump = v
	}
	if v, ok := config["pass"].(int); ok {
		m.Pass = v
	}

	m.Persist = true // default true
	if v, ok := config["persist"].(bool); ok {
		m.Persist = v
	}

	m.reqs = state.ParseRequisites(config)
	return m, nil
}

func (m *MountMounted) Name() string           { return "mount.mounted:" + m.id }
func (m *MountMounted) Reqs() state.Requisites { return m.reqs }

func (m *MountMounted) Check(ctx context.Context) (state.CheckResult, error) {
	mounted, err := m.mount.IsMounted(ctx, m.MountPoint)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("mount.mounted: check %s: %w", m.MountPoint, err)
	}

	if !mounted {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("%s is not mounted", m.MountPoint),
		}, nil
	}

	if m.Persist {
		fstabEntry, err := m.mount.GetFstab(ctx, m.MountPoint)
		if err != nil {
			return state.CheckResult{}, fmt.Errorf("mount.mounted: get fstab %s: %w", m.MountPoint, err)
		}
		if fstabEntry == nil {
			return state.CheckResult{
				NeedsChange: true,
				Diff:        fmt.Sprintf("%s is mounted but not in fstab", m.MountPoint),
			}, nil
		}
		if fstabEntry.Device != m.Device || fstabEntry.FSType != m.FSType || fstabEntry.Options != m.Options {
			return state.CheckResult{
				NeedsChange: true,
				Diff:        fmt.Sprintf("%s fstab entry does not match desired config", m.MountPoint),
			}, nil
		}
	}

	return state.CheckResult{NeedsChange: false}, nil
}

func (m *MountMounted) Apply(ctx context.Context) (state.ApplyResult, error) {
	entry := exec.MountEntry{
		Device:     m.Device,
		MountPoint: m.MountPoint,
		FSType:     m.FSType,
		Options:    m.Options,
		Dump:       m.Dump,
		Pass:       m.Pass,
	}

	if m.Persist {
		if err := m.mount.SetFstab(ctx, entry); err != nil {
			return state.ApplyResult{}, fmt.Errorf("mount.mounted: set fstab %s: %w", m.MountPoint, err)
		}
		m.appliedFstab = true
	}

	mounted, err := m.mount.IsMounted(ctx, m.MountPoint)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("mount.mounted: check mounted %s: %w", m.MountPoint, err)
	}
	if !mounted {
		if err := m.mount.Mount(ctx, entry); err != nil {
			return state.ApplyResult{}, fmt.Errorf("mount.mounted: mount %s: %w", m.MountPoint, err)
		}
		m.appliedMount = true
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("mounted %s (%s) at %s", m.Device, m.FSType, m.MountPoint),
		Details: map[string]string{
			"device":     m.Device,
			"mountpoint": m.MountPoint,
			"fstype":     m.FSType,
			"options":    m.Options,
			"persisted":  fmt.Sprintf("%v", m.Persist),
		},
	}, nil
}

func (m *MountMounted) Revert(ctx context.Context) (state.ApplyResult, error) {
	if m.appliedMount {
		if err := m.mount.Unmount(ctx, m.MountPoint); err != nil {
			return state.ApplyResult{}, fmt.Errorf("mount.mounted: revert unmount %s: %w", m.MountPoint, err)
		}
	}
	if m.appliedFstab {
		if err := m.mount.RemoveFstab(ctx, m.MountPoint); err != nil {
			return state.ApplyResult{}, fmt.Errorf("mount.mounted: revert remove fstab %s: %w", m.MountPoint, err)
		}
	}
	return state.ApplyResult{
		Changed: m.appliedMount || m.appliedFstab,
		Diff:    fmt.Sprintf("unmounted %s and removed fstab entry (revert)", m.MountPoint),
	}, nil
}
