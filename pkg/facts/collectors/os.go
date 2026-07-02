// Package collectors provides built-in fact collectors for system information.
package collectors

import (
	"context"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v4/host"
)

// OS collects operating system facts.
type OS struct{}

func (OS) Name() string            { return "os" }
func (OS) Interval() time.Duration { return 0 }

func (OS) Collect(ctx context.Context) (map[string]any, error) {
	info, err := host.InfoWithContext(ctx)
	if err != nil {
		// Fallback to runtime-only data.
		return map[string]any{
			"name":   runtime.GOOS,
			"arch":   runtime.GOARCH,
			"family": runtime.GOOS,
		}, nil
	}

	return map[string]any{
		"name":             info.OS,
		"family":           info.PlatformFamily,
		"platform":         info.Platform,
		"platform_version": info.PlatformVersion,
		"arch":             runtime.GOARCH,
		"kernel":           info.KernelVersion,
		"kernel_arch":      info.KernelArch,
		"uptime":           info.Uptime,
		"boot_time":        info.BootTime,
	}, nil
}
