package collectors

import (
	"context"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
)

// CPU collects CPU-related facts.
type CPU struct{}

func (CPU) Name() string            { return "cpu" }
func (CPU) Interval() time.Duration { return 0 }

func (CPU) Collect(ctx context.Context) (map[string]any, error) {
	result := map[string]any{
		"count":         runtime.NumCPU(),
		"logical_count": runtime.NumCPU(),
	}

	// Physical core count.
	physical, err := cpu.CountsWithContext(ctx, false)
	if err == nil {
		result["physical_count"] = physical
	}

	// CPU model info from first processor.
	infos, err := cpu.InfoWithContext(ctx)
	if err == nil && len(infos) > 0 {
		first := infos[0]
		result["model"] = first.ModelName
		result["vendor"] = first.VendorID
		result["mhz"] = first.Mhz
		result["cache_size"] = first.CacheSize
	}

	return result, nil
}
