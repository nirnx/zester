package peeld

import (
	"context"
	"time"

	"github.com/ptorbus/zester/internal/config"
	"github.com/ptorbus/zester/pkg/job"
	"github.com/ptorbus/zester/pkg/proto"
	"github.com/ptorbus/zester/pkg/schedule"
)

// schedExec is the scheduler's ExecFn. Scheduled single-module runs carry no
// separate state ID; state modules that need one read it from args (e.g.
// name/path/state — and a scheduled event.send reads its tag from tag=).
func (a *Agent) schedExec(ctx context.Context, module string, args map[string]any) schedule.ExecResult {
	resp, err := a.execModule(ctx, proto.ExecRequest{Module: module, Args: args})
	if err != nil {
		return schedule.ExecResult{Error: err.Error()}
	}
	if resp.Error != "" {
		return schedule.ExecResult{Error: resp.Error, ResultData: resp}
	}
	return schedule.ExecResult{Success: resp.Success, ResultData: resp}
}

// schedReturn is the scheduler's ReturnFn: it publishes the result on the
// peel-scoped schedule subject. The job-events stream captures it durably and
// the master persists it as a synthetic job so it appears in "zester job
// list". Peels deliberately have no write access to the job KV buckets.
func (a *Agent) schedReturn(entry schedule.Entry, result schedule.ExecResult) {
	res := job.ScheduledResult{
		JID:        job.NewJID(),
		Entry:      entry.Name,
		Module:     entry.Module,
		Args:       entry.Args,
		Success:    result.Success,
		Error:      result.Error,
		ReturnData: result.ResultData,
		Duration:   result.Duration,
		Timestamp:  time.Now().UTC(),
	}
	if err := job.PublishScheduledResult(a.ps, a.peelID, res); err != nil {
		a.logger.Error("schedule: publish scheduled result", "entry", entry.Name, "error", err)
		return
	}
	a.logger.Info("schedule: result published", "entry", entry.Name, "jid", res.JID, "success", result.Success)
}

// scheduleRawEntries converts peel.yaml schedule entries into the schedule
// package's raw form for parsing/validation.
func scheduleRawEntries(entries map[string]config.PeelScheduleEntry) map[string]schedule.RawEntry {
	raw := make(map[string]schedule.RawEntry, len(entries))
	for name, se := range entries {
		raw[name] = schedule.RawEntry{
			Module:     se.Module,
			Args:       se.Args,
			Interval:   se.Interval,
			Cron:       se.Cron,
			Splay:      se.Splay,
			MaxRunning: se.MaxRunning,
			RunOnStart: se.RunOnStart,
			ReturnJob:  se.ReturnJob,
			Enabled:    se.Enabled,
		}
	}
	return raw
}
