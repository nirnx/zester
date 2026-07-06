package schedule_test

import (
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/schedule"
)

func TestEntryValidate(t *testing.T) {
	tests := []struct {
		name    string
		entry   schedule.Entry
		wantErr bool
	}{
		{
			name: "valid interval entry",
			entry: schedule.Entry{
				Name:     "test",
				Module:   "cmd.run",
				Interval: 5 * time.Minute,
				Enabled:  true,
			},
			wantErr: false,
		},
		{
			name: "valid cron entry",
			entry: schedule.Entry{
				Name:    "test",
				Module:  "cmd.run",
				Cron:    "0 * * * *",
				Enabled: true,
			},
			wantErr: false,
		},
		{
			name: "missing module",
			entry: schedule.Entry{
				Name:     "test",
				Interval: 5 * time.Minute,
				Enabled:  true,
			},
			wantErr: true,
		},
		{
			name: "missing interval and cron",
			entry: schedule.Entry{
				Name:    "test",
				Module:  "cmd.run",
				Enabled: true,
			},
			wantErr: true,
		},
		{
			name: "both interval and cron set",
			entry: schedule.Entry{
				Name:     "test",
				Module:   "cmd.run",
				Interval: 5 * time.Minute,
				Cron:     "0 * * * *",
				Enabled:  true,
			},
			wantErr: true,
		},
		{
			name: "negative interval",
			entry: schedule.Entry{
				Name:     "test",
				Module:   "cmd.run",
				Interval: -5 * time.Minute,
				Enabled:  true,
			},
			wantErr: true,
		},
		{
			name: "invalid cron expression",
			entry: schedule.Entry{
				Name:    "test",
				Module:  "cmd.run",
				Cron:    "not-a-cron",
				Enabled: true,
			},
			wantErr: true,
		},
		{
			name: "negative maxrunning",
			entry: schedule.Entry{
				Name:       "test",
				Module:     "cmd.run",
				Interval:   5 * time.Minute,
				MaxRunning: -1,
				Enabled:    true,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.entry.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
