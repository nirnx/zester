package collectors

import (
	"context"
	"time"

	"github.com/shirou/gopsutil/v4/mem"
)

// Memory collects memory-related facts.
type Memory struct{}

func (Memory) Name() string            { return "memory" }
func (Memory) Interval() time.Duration { return 5 * time.Minute }

func (Memory) Collect(ctx context.Context) (map[string]any, error) {
	v, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return nil, err
	}

	result := map[string]any{
		"total":        v.Total,
		"available":    v.Available,
		"used":         v.Used,
		"used_percent": v.UsedPercent,
		"free":         v.Free,
	}

	swap, err := mem.SwapMemoryWithContext(ctx)
	if err == nil {
		result["swap_total"] = swap.Total
		result["swap_used"] = swap.Used
		result["swap_free"] = swap.Free
	}

	return result, nil
}
