package collectors

import (
	"context"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

// Disk collects disk and mount point facts.
type Disk struct{}

func (Disk) Name() string            { return "disk" }
func (Disk) Interval() time.Duration { return 5 * time.Minute }

func (Disk) Collect(ctx context.Context) (map[string]any, error) {
	parts, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		return nil, err
	}

	var mounts []map[string]any
	for _, p := range parts {
		mount := map[string]any{
			"device":     p.Device,
			"mountpoint": p.Mountpoint,
			"fstype":     p.Fstype,
			"opts":       p.Opts,
		}

		usage, err := disk.UsageWithContext(ctx, p.Mountpoint)
		if err == nil {
			mount["total"] = usage.Total
			mount["used"] = usage.Used
			mount["free"] = usage.Free
			mount["used_percent"] = usage.UsedPercent
		}

		mounts = append(mounts, mount)
	}

	return map[string]any{
		"mounts": mounts,
	}, nil
}
