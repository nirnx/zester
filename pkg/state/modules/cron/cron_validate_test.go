package cronmod

import (
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
)

// TestCronPresentRejectsInjectingValues pins the builder-tail validation: a
// crontab is line-oriented with five whitespace-delimited schedule columns,
// so a space inside a schedule field shifts the columns and a newline in any
// value splices a second, unmanaged job into the crontab. The builder must
// refuse to build, naming the offending parameter.
func TestCronPresentRejectsInjectingValues(t *testing.T) {
	build := NewCronPresentBuilder(testCronMctx(exectest.NewFakeCronExec()), modschema.DecodeOptions{})
	base := func(over map[string]any) map[string]any {
		cfg := map[string]any{"command": "/usr/bin/backup.sh"}
		for k, v := range over {
			cfg[k] = v
		}
		return cfg
	}

	bad := []struct {
		name   string
		id     string
		config map[string]any
		field  string
	}{
		{"space in minute", "backup", base(map[string]any{"minute": "5 *"}), "minute"},
		{"newline in minute", "backup", base(map[string]any{"minute": "5\n* * * * * /evil"}), "minute"},
		{"tab in hour", "backup", base(map[string]any{"hour": "2\t*"}), "hour"},
		{"space in daymonth", "backup", base(map[string]any{"daymonth": "1 15"}), "daymonth"},
		{"space in month", "backup", base(map[string]any{"month": "1 6"}), "month"},
		{"space in dayweek", "backup", base(map[string]any{"dayweek": "1 5"}), "dayweek"},
		{"newline in command", "backup", base(map[string]any{"command": "/usr/bin/true\n* * * * * /evil"}), "command"},
		{"carriage return in command", "backup", base(map[string]any{"command": "/usr/bin/true\r"}), "command"},
		{"newline in name", "backup\n* * * * * /evil", base(nil), "name"},
		{"newline in explicit name", "backup", base(map[string]any{"name": "lbl\n"}), "name"},
		{"newline in user", "backup", base(map[string]any{"user": "root\n"}), "user"},
	}
	for _, tc := range bad {
		_, err := build(tc.id, tc.config)
		if err == nil {
			t.Errorf("%s: expected builder error, got nil", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.field+":") {
			t.Errorf("%s: error %q should name the %q parameter", tc.name, err, tc.field)
		}
	}
}

func TestCronPresentAcceptsOrdinaryValues(t *testing.T) {
	build := NewCronPresentBuilder(testCronMctx(exectest.NewFakeCronExec()), modschema.DecodeOptions{})
	good := []map[string]any{
		{"command": "/usr/bin/backup.sh"},
		{"command": "/usr/bin/backup.sh --flag 'a b'   --verbose", "minute": "*/5", "hour": "1-3", "daymonth": "*", "month": "1,6", "dayweek": "mon-fri"},
		{"command": "/usr/bin/backup.sh", "minute": 5, "user": "postgres"},
		{"command": "/usr/bin/backup.sh", "name": "nightly db dump"},
	}
	for _, cfg := range good {
		if _, err := build("backup", cfg); err != nil {
			t.Errorf("config %v: unexpected builder error %v", cfg, err)
		}
	}
}
