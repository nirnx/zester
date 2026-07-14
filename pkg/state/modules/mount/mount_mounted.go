package mountmod

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
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
//
// MountMounted is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module). `device`
// is `required`; `fstype`/`opts` carry EAGER `default=ext4`/`default=defaults`
// and `persist` an eager `default=true`, reproducing the legacy construction-time
// defaults — but through the uniform decoder, so a msgpack-sized-int / numeric-
// string / finite-float `dump`/`pass` now coerces to the integer (BD-1/BD-2/BD-6)
// and an integer/truthy-string `persist` is honored (BD-2/BD-7) where the legacy
// `.(int)`/`.(bool)` assertions silently dropped them. This is a parameter-decode
// migration ONLY: Check/Apply/Revert (and the deferred live-facet policy above)
// are unchanged. The unexported runtime fields (id, reqs, mount, revert memos)
// are untagged, so the schema compiler skips them.
type MountMounted struct {
	id   string
	reqs state.Requisites

	// MountPoint is the mount point path; it defaults to the state ID.
	MountPoint string `zester:"name,primary" usage:"mount point path; defaults to the state ID"`
	// Device is the device/source to mount; required.
	Device string `zester:"device,required" usage:"device or source to mount (a block device, UUID=/LABEL= alias, or NFS export); required"`
	// FSType is the filesystem type; defaults to ext4.
	FSType string `zester:"fstype,default=ext4" usage:"filesystem type (ext4, xfs, nfs, …); defaults to ext4"`
	// Options are the mount options recorded in fstab; default defaults.
	Options string `zester:"opts,default=defaults" usage:"mount options recorded in fstab (comma-separated); defaults to \"defaults\""`
	// Dump is the fstab dump field (fifth column).
	Dump int `zester:"dump" usage:"fstab dump field (fifth column); defaults to 0"`
	// Pass is the fstab fsck pass-order field (sixth column).
	Pass int `zester:"pass" usage:"fstab fsck pass-order field (sixth column); defaults to 0"`
	// Persist records the entry in fstab so it survives a reboot; defaults to true.
	Persist bool `zester:"persist,default=true" usage:"record the entry in fstab so it survives a reboot; defaults to true; a boolean that also accepts the integers 1 (true) and 0 (false)"`

	mount exec.MountExec

	// applied tracks what was done during Apply for Revert.
	appliedMount bool
	appliedFstab bool
}

// mountMountedSpec is the compiled schema + documentation for mount.mounted. Its
// Doc is drift-corrected against the live Check/Apply/Revert behavior — notably
// the deliberate blindness to the live mount's device/fstype/options (the audit's
// open live-facet-normalization item), which this migration does not change.
var mountMountedSpec = regdef.MustSpec("mount.mounted", modschema.KindState, MountMounted{}, modschema.Doc{
	Summary: "Ensure a filesystem is mounted at a mount point and recorded in fstab.",
	Description: "`mount.mounted` ensures SOMETHING is mounted at the mount point and, with `persist` " +
		"(the default), that the declared fstab entry — `device`, `fstype`, `opts`, `dump`, and `pass` — " +
		"is present. The mount point defaults to the state ID. `device` is required; `fstype` defaults to " +
		"`ext4` and `opts` to `defaults`.\n\n" +
		"**Deliberately not compared:** the LIVE mount's device/fstype/options. Comparing the declared " +
		"fstab-language configuration against the kernel's `/proc/mounts` view needs Salt-style " +
		"normalization (fstab-only options like `nofail`/`_netdev` never appear in `/proc/mounts`, " +
		"negotiated fstypes like `nfs`→`nfs4`, `UUID=`/`LABEL=` device aliasing) — without it every " +
		"mismatch would escalate to an unmount+remount of a live production filesystem on every run. That " +
		"facet is deferred until the normalization exists; silent blindness beats production unmounts.",
	Effects: modschema.Effects{
		Check: "Probes whether anything is mounted at the mount point (by reading `/proc/mounts`). With " +
			"`persist` (the default) it also verifies the `/etc/fstab` entry EXACTLY matches the declared " +
			"device, fstype, options, dump, and " +
			"pass. Reports a change when the mount point is unmounted, or when the fstab entry is missing " +
			"or differs. It deliberately does NOT compare the live mount's own device/fstype/options (see " +
			"the deferred live-facet note), so a mount already serving the point never re-triggers a churn.",
		Apply: "Re-probes the mount state (a self-contained flow: a watch-forced Apply bypasses Check). " +
			"With `persist`, it writes or updates the fstab entry when it is missing or mismatched. It " +
			"mounts the device ONLY when nothing is currently mounted at the point — an existing mount is " +
			"never unmounted or remounted, even one that would look \"wrong\" under a naive live " +
			"comparison. A fully converged entry (mounted, fstab matches) is a clean no-op. Reports the " +
			"device, mount point, fstype, options, and persist flag in its details.",
		Revert: "Undoes only what this run's Apply did: it unmounts a filesystem THIS Apply mounted and " +
			"removes an fstab entry THIS Apply added — nothing else. A fresh instance (a standalone " +
			"revert) recorded nothing and is an explicit clean no-op; it never unmounts a live filesystem " +
			"or edits fstab for a mount it did not create.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Mount a data volume and record it in fstab",
			Kind:        "state",
			Explanation: "The mount point defaults to the state ID; device is required; persist (default true) writes the fstab entry.",
			Code: "/mnt/data:\n  mount.mounted:\n    - device: /dev/sdb1\n    - fstype: xfs\n" +
				"    - opts: defaults,noatime\n    - pass: 2\n",
		},
		{
			Title:       "Mount an NFS export",
			Kind:        "state",
			Explanation: "fstab-only options like nofail/_netdev are recorded in fstab but never compared against the live mount.",
			Code: "nfs-share:\n  mount.mounted:\n    - name: /mnt/share\n    - device: 10.0.0.5:/vol\n" +
				"    - fstype: nfs\n    - opts: defaults,nofail,_netdev\n    - require:\n" +
				"      - \"pkg.installed:nfs-common\"\n",
		},
		{
			Title:       "Mount a device ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the mount point; device is a key=value.",
			Code:        "zester 'db*' mount.mounted /mnt/data device=/dev/sdb1 fstype=xfs",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "warning",
			Title: "The live mount's config is not compared",
			Body: "mount.mounted compares only mount-point PRESENCE and the fstab entry — never the live " +
				"mount's own device/fstype/options. Reconciling fstab-language configuration with the " +
				"kernel's `/proc/mounts` view needs normalization (fstab-only options, negotiated fstypes, " +
				"UUID=/LABEL= aliasing) that does not yet exist; without it, a naive comparison would " +
				"perpetually escalate to unmounting a live production filesystem. Apply mounts only when " +
				"nothing is mounted at the point and NEVER unmounts.",
		},
		{
			Level: "info",
			Title: "persist defaults to true; integer dump/pass are honored",
			Body: "`persist` defaults to true (the fstab entry is written so the mount survives a reboot); " +
				"set `persist: false` to mount without editing fstab. Under the uniform decoder an integer " +
				"`dump`/`pass` (including a reactor-dispatched msgpack-sized integer) coerces to the integer " +
				"field, and an integer or truthy-string `persist` (`1`, `\"true\"`) is honored where the " +
				"legacy `.(int)`/`.(bool)` assertions silently dropped them.",
		},
	},
	Divergences: []string{"BD-1", "BD-2", "BD-6", "BD-7"},
})

// NewMountMountedBuilder returns a state.Builder that creates MountMounted states
// using the given ModuleContext's mount provider. Decode policy (unknown-key
// handling, reserved keys) is threaded via opts; the peel supplies it through
// modules.RegisterAll.
func NewMountMountedBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Mount == nil {
			return nil, fmt.Errorf("mount.mounted: no mount provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it — assigning them before would be
		// overwritten by the committed scratch value.
		m := &MountMounted{}
		if _, err := mountMountedSpec.Decode(id, config, m, opts); err != nil {
			return nil, fmt.Errorf("mount.mounted: %w", err)
		}
		m.id = id
		m.mount = mctx.Mount
		m.reqs = state.ParseRequisites(config)
		return m, nil
	}
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
