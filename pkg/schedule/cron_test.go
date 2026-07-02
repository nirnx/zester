package schedule_test

import (
	"testing"
	"time"

	"github.com/ptorbus/zester/pkg/schedule"
)

func TestParseCron(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{"every hour", "0 * * * *", false},
		{"every day at 2:30", "30 2 * * *", false},
		{"first of month", "0 0 1 * *", false},
		{"every minute", "* * * * *", false},
		{"invalid expression", "not-a-cron", true},
		{"too few fields", "* * *", true},
		{"empty string", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, err := schedule.ParseCron(tt.expr)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseCron(%q) error = %v, wantErr %v", tt.expr, err, tt.wantErr)
			}
			if !tt.wantErr && sched == nil {
				t.Errorf("ParseCron(%q) returned nil schedule without error", tt.expr)
			}
		})
	}
}

func TestNextCron(t *testing.T) {
	// Reference time: 2024-01-15 10:30:00 UTC (Monday)
	ref := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)

	tests := []struct {
		name    string
		expr    string
		after   time.Time
		wantErr bool
		checkFn func(t *testing.T, next time.Time)
	}{
		{
			name:    "every hour fires at next hour",
			expr:    "0 * * * *",
			after:   ref,
			wantErr: false,
			checkFn: func(t *testing.T, next time.Time) {
				if !next.After(ref) {
					t.Errorf("next %v is not after ref %v", next, ref)
				}
				if next.Minute() != 0 {
					t.Errorf("expected minute 0, got %d", next.Minute())
				}
				if next.Hour() != 11 {
					t.Errorf("expected hour 11, got %d", next.Hour())
				}
			},
		},
		{
			name:    "daily at 2:30 fires next day",
			expr:    "30 2 * * *",
			after:   ref,
			wantErr: false,
			checkFn: func(t *testing.T, next time.Time) {
				if !next.After(ref) {
					t.Errorf("next %v is not after ref %v", next, ref)
				}
				if next.Hour() != 2 || next.Minute() != 30 {
					t.Errorf("expected 02:30, got %02d:%02d", next.Hour(), next.Minute())
				}
			},
		},
		{
			name:    "first of month fires next month",
			expr:    "0 0 1 * *",
			after:   ref,
			wantErr: false,
			checkFn: func(t *testing.T, next time.Time) {
				if !next.After(ref) {
					t.Errorf("next %v is not after ref %v", next, ref)
				}
				if next.Day() != 1 {
					t.Errorf("expected day 1, got %d", next.Day())
				}
			},
		},
		{
			name:    "invalid expression returns error",
			expr:    "bad-cron",
			after:   ref,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, err := schedule.NextCron(tt.expr, tt.after)
			if (err != nil) != tt.wantErr {
				t.Errorf("NextCron(%q) error = %v, wantErr %v", tt.expr, err, tt.wantErr)
				return
			}
			if !tt.wantErr && tt.checkFn != nil {
				tt.checkFn(t, next)
			}
		})
	}
}
