package schedule

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// cronParser is a standard 5-field cron parser (minute, hour, dom, month, dow).
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// ParseCron validates a cron expression and returns the parsed schedule.
func ParseCron(expr string) (cron.Schedule, error) {
	sched, err := cronParser.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	return sched, nil
}

// NextCron returns the next fire time after the given time for a cron expression.
func NextCron(expr string, after time.Time) (time.Time, error) {
	sched, err := ParseCron(expr)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(after), nil
}
