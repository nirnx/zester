package exec

import (
	"strings"
	"testing"
)

func TestValidateCronUser(t *testing.T) {
	valid := []string{"", "root", "deploy", "svc_acct", "user.name", "a@example", "machine$", "www-data", "_apt", "u1"}
	for _, u := range valid {
		if err := validateCronUser(u); err != nil {
			t.Errorf("validateCronUser(%q): unexpected error %v", u, err)
		}
	}

	invalid := []string{
		" ",                      // whitespace-only is not "current user"
		"root; touch /tmp/pwned", // command injection via the -u value
		"root && id",
		"a b",   // whitespace splits the argument
		"-flag", // option-looking name
		"$(id)",
		"`id`",
		"a\nb", // newline
		"a\tb", // tab
		"ro\x00ot",
		"user|cat",
		"dir/name",
		"héllo", // non-ASCII is outside the portable account-name set
	}
	for _, u := range invalid {
		err := validateCronUser(u)
		if err == nil {
			t.Errorf("validateCronUser(%q): expected error, got nil", u)
			continue
		}
		if !strings.Contains(err.Error(), "invalid user name") {
			t.Errorf("validateCronUser(%q): error %q should name the problem", u, err)
		}
	}
}

func TestCronEntryValidate(t *testing.T) {
	good := CronEntry{Minute: "*/5", Hour: "1-3", DayOfMonth: "*", Month: "1,6", DayOfWeek: "mon-fri",
		Command: "/usr/local/bin/job --flag 'a b'", Comment: "nightly job", User: "root"}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid entry rejected: %v", err)
	}
	// Empty schedule fields are serialized as `*` — legal.
	if err := (CronEntry{Command: "/bin/true"}).Validate(); err != nil {
		t.Fatalf("empty-schedule entry rejected: %v", err)
	}

	bad := []struct {
		name  string
		entry CronEntry
	}{
		{"space in minute", CronEntry{Minute: "5 *", Command: "/x"}},
		{"tab in hour", CronEntry{Hour: "1\t2", Command: "/x"}},
		{"newline in day-of-month", CronEntry{DayOfMonth: "1\n", Command: "/x"}},
		{"space in month", CronEntry{Month: "1 2", Command: "/x"}},
		{"newline in day-of-week", CronEntry{DayOfWeek: "*\n* * * * * /evil", Command: "/x"}},
		{"newline in command", CronEntry{Command: "/x\n* * * * * /evil"}},
		{"carriage return in command", CronEntry{Command: "/x\r"}},
		{"NUL in command", CronEntry{Command: "/x\x00"}},
		{"newline in comment", CronEntry{Command: "/x", Comment: "label\n* * * * * /evil"}},
		{"newline in user", CronEntry{Command: "/x", User: "root\n"}},
	}
	for _, tc := range bad {
		if err := tc.entry.Validate(); err == nil {
			t.Errorf("%s: expected Validate error, got nil", tc.name)
		}
	}
}
