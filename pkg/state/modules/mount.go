package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// MountMounted implements the mount.mounted state.
// It ensures SOMETHING is mounted at the mount point and that the declared
// entry is present in fstab (including dump/pass).
//
// Deliberately NOT compared: the live mount's device/fstype/options. Comparing
// the declared fstab-language configuration against the kernel's /proc/mounts
// view needs Salt-style normalization (fstab-only options like nofail/_netdev
// never appear in /proc/mounts, negotiated fstypes like nfs→nfs4, UUID=/LABEL=
// device aliasing) — without it every mismatch would escalate to an
// unmount+remount of a live production filesystem on every run. That facet is
// deferred until the normalization exists; silent blindness beats production
// unmounts. Apply therefore mounts ONLY when nothing is mounted at the
// mount point and never unmounts.
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

// fstabMatches reports whether the fstab entry matches the declared state
// exactly, including dump and pass.
func (m *MountMounted) fstabMatches(e *exec.MountEntry) bool {
	return e.Device == m.Device && e.FSType == m.FSType && e.Options == m.Options &&
		e.Dump == m.Dump && e.Pass == m.Pass
}

func (m *MountMounted) Check(ctx context.Context) (state.CheckResult, error) {
	mounted, err := m.mount.IsMounted(ctx, m.MountPoint)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("mount.mounted: check %s: %w", m.MountPoint, err)
	}

	var diffs []string
	if !mounted {
		diffs = append(diffs, fmt.Sprintf("%s is not mounted", m.MountPoint))
	}

	if m.Persist {
		fstabEntry, err := m.mount.GetFstab(ctx, m.MountPoint)
		if err != nil {
			return state.CheckResult{}, fmt.Errorf("mount.mounted: get fstab %s: %w", m.MountPoint, err)
		}
		if fstabEntry == nil {
			diffs = append(diffs, fmt.Sprintf("%s is not in fstab", m.MountPoint))
		} else if !m.fstabMatches(fstabEntry) {
			diffs = append(diffs, fmt.Sprintf("%s fstab entry does not match desired config", m.MountPoint))
		}
	}

	if len(diffs) > 0 {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        strings.Join(diffs, "; "),
		}, nil
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

	mounted, err := m.mount.IsMounted(ctx, m.MountPoint)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("mount.mounted: check %s: %w", m.MountPoint, err)
	}

	var actions []string

	if m.Persist {
		current, err := m.mount.GetFstab(ctx, m.MountPoint)
		if err != nil {
			return state.ApplyResult{}, fmt.Errorf("mount.mounted: get fstab %s: %w", m.MountPoint, err)
		}
		if current == nil || !m.fstabMatches(current) {
			if err := m.mount.SetFstab(ctx, entry); err != nil {
				return state.ApplyResult{}, fmt.Errorf("mount.mounted: set fstab %s: %w", m.MountPoint, err)
			}
			m.appliedFstab = true
			actions = append(actions, "updated fstab entry")
		}
	}

	// Mount ONLY when nothing is mounted at the mount point — an existing
	// mount is never unmounted or remounted (see the type doc: the live
	// config facet is deferred).
	if !mounted {
		if err := m.mount.Mount(ctx, entry); err != nil {
			return state.ApplyResult{}, fmt.Errorf("mount.mounted: mount %s: %w", m.MountPoint, err)
		}
		m.appliedMount = true
		actions = append(actions, fmt.Sprintf("mounted %s (%s)", m.Device, m.FSType))
	}

	if len(actions) == 0 {
		return state.ApplyResult{
			Changed: false,
			Diff:    fmt.Sprintf("%s already mounted with desired configuration", m.MountPoint),
		}, nil
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("%s at %s", strings.Join(actions, "; "), m.MountPoint),
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
	var acts []string
	if m.appliedMount {
		if err := m.mount.Unmount(ctx, m.MountPoint); err != nil {
			return state.ApplyResult{}, fmt.Errorf("mount.mounted: revert unmount %s: %w", m.MountPoint, err)
		}
		acts = append(acts, fmt.Sprintf("unmounted %s", m.MountPoint))
	}
	if m.appliedFstab {
		if err := m.mount.RemoveFstab(ctx, m.MountPoint); err != nil {
			return state.ApplyResult{}, fmt.Errorf("mount.mounted: revert remove fstab %s: %w", m.MountPoint, err)
		}
		acts = append(acts, "removed fstab entry")
	}
	if len(acts) == 0 {
		return state.ApplyResult{
			Changed: false,
			Diff:    "nothing to revert (no apply recorded in this run)",
		}, nil
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    strings.Join(acts, " and ") + " (revert)",
	}, nil
}
