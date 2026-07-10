package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// MountMounted implements the mount.mounted state.
// It ensures the DECLARED filesystem is live-mounted at the mount point
// (device and fstype compared exactly, declared options as a subset of the
// live option set) and optionally present in fstab.
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
	// file reads the kernel mount table (exec.ProcMountsPath): MountExec
	// exposes only IsMounted (presence), not the live entry, so device/
	// fstype/options drift is verified via the file provider — still the
	// exec layer, mirroring FstabProvider's own /proc/mounts read.
	file exec.FileExec

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
		if mctx.File == nil {
			return nil, fmt.Errorf("mount.mounted: no file provider available (required to read %s)", exec.ProcMountsPath)
		}
		return newMountMounted(id, config, mctx.Mount, mctx.File)
	}
}

func newMountMounted(id string, config map[string]any, mount exec.MountExec, file exec.FileExec) (state.State, error) {
	m := &MountMounted{id: id, mount: mount, file: file}

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

// procMountsUnescape decodes the octal escapes the kernel uses in
// /proc/mounts fields (\040 space, \011 tab, \012 newline, \134 backslash).
var procMountsUnescape = strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)

// liveMount returns the kernel mount table entry for the mount point, or nil
// when nothing is mounted there. An unreadable (including absent) mount table
// fails the phase — mount state cannot be determined without it, and guessing
// "not mounted" could trigger a spurious mount.
func (m *MountMounted) liveMount(ctx context.Context) (*exec.MountEntry, error) {
	data, err := m.file.ReadFile(ctx, exec.ProcMountsPath)
	if err != nil {
		return nil, fmt.Errorf("mount.mounted: read %s: %w", exec.ProcMountsPath, err)
	}
	var found *exec.MountEntry
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		if procMountsUnescape.Replace(fields[1]) != m.MountPoint {
			continue
		}
		// Later lines shadow earlier ones (stacked mounts): keep the last.
		found = &exec.MountEntry{
			Device:     procMountsUnescape.Replace(fields[0]),
			MountPoint: m.MountPoint,
			FSType:     fields[2],
			Options:    procMountsUnescape.Replace(fields[3]),
		}
	}
	return found, nil
}

// liveMatches reports whether the live mount satisfies the declared state.
// Device and fstype compare exactly; declared options must be a SUBSET of the
// live option set — the kernel adds its own (rw, relatime, ...) and requiring
// equality would churn forever — with "defaults" imposing no requirement.
func (m *MountMounted) liveMatches(live *exec.MountEntry) (bool, string) {
	if live.Device != m.Device {
		return false, fmt.Sprintf("device %s != %s", live.Device, m.Device)
	}
	if live.FSType != m.FSType {
		return false, fmt.Sprintf("fstype %s != %s", live.FSType, m.FSType)
	}
	liveOpts := make(map[string]bool)
	for _, o := range strings.Split(live.Options, ",") {
		liveOpts[o] = true
	}
	for _, o := range strings.Split(m.Options, ",") {
		if o == "" || o == "defaults" {
			continue
		}
		if !liveOpts[o] {
			return false, fmt.Sprintf("option %q not active", o)
		}
	}
	return true, ""
}

// fstabMatches reports whether the fstab entry matches the declared state
// exactly, including dump and pass.
func (m *MountMounted) fstabMatches(e *exec.MountEntry) bool {
	return e.Device == m.Device && e.FSType == m.FSType && e.Options == m.Options &&
		e.Dump == m.Dump && e.Pass == m.Pass
}

func (m *MountMounted) Check(ctx context.Context) (state.CheckResult, error) {
	live, err := m.liveMount(ctx)
	if err != nil {
		return state.CheckResult{}, err
	}

	var diffs []string
	if live == nil {
		diffs = append(diffs, fmt.Sprintf("%s is not mounted", m.MountPoint))
	} else if ok, why := m.liveMatches(live); !ok {
		diffs = append(diffs, fmt.Sprintf("%s live mount does not match: %s", m.MountPoint, why))
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

	// Read the mount table first: an unreadable table fails the phase
	// BEFORE any mutation.
	live, err := m.liveMount(ctx)
	if err != nil {
		return state.ApplyResult{}, err
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

	switch {
	case live == nil:
		if err := m.mount.Mount(ctx, entry); err != nil {
			return state.ApplyResult{}, fmt.Errorf("mount.mounted: mount %s: %w", m.MountPoint, err)
		}
		m.appliedMount = true
		actions = append(actions, fmt.Sprintf("mounted %s (%s)", m.Device, m.FSType))
	default:
		if ok, why := m.liveMatches(live); !ok {
			// The live mount does not match the declared state (wrong device,
			// fstype, or missing options) — remount with the desired config.
			// Not memoized for Revert: the prior mount configuration cannot be
			// restored faithfully (mirrors service.running's restart).
			if err := m.mount.Unmount(ctx, m.MountPoint); err != nil {
				return state.ApplyResult{}, fmt.Errorf("mount.mounted: unmount %s for remount: %w", m.MountPoint, err)
			}
			if err := m.mount.Mount(ctx, entry); err != nil {
				return state.ApplyResult{}, fmt.Errorf("mount.mounted: mount %s: %w", m.MountPoint, err)
			}
			actions = append(actions, fmt.Sprintf("remounted %s (%s)", m.Device, why))
		}
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
