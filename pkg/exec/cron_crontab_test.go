package exec_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
)

// crontabWrites extracts the crontab content written via the provider's
// `printf '%s' '<content>' | crontab ...` shell pipeline from the recorded
// fake calls (one string per write, in order).
func crontabWrites(cmd *exectest.FakeCommandExec) []string {
	var writes []string
	for _, call := range cmd.Calls() {
		if !call.Shell || !strings.HasPrefix(call.Command, "printf") {
			continue
		}
		writes = append(writes, call.Command)
	}
	return writes
}

func TestCrontabProviderAdoptsHandCommentedLine(t *testing.T) {
	// Round-2 regression (finding: comment-keyed identity): a hand-written
	// entry annotated with an ordinary descriptive comment must be ADOPTED by
	// a labeled Set with the same command — not duplicated. The old parse
	// attached "# nightly db dump" as the entry's Comment, which blocked the
	// label-less adoption fallback and appended a second line: dump.sh then
	// ran twice at 02:00, forever, while Check reported compliant.
	cmd := exectest.NewFakeCommandExec()
	cmd.SetResult("crontab", &exec.CommandResult{
		Stdout: "# nightly db dump\n0 2 * * * /usr/local/bin/dump.sh\n",
	}, nil)
	p := exec.NewCrontabProvider(cmd)

	err := p.Set(context.Background(), "root", exec.CronEntry{
		Minute: "0", Hour: "2", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		Command: "/usr/local/bin/dump.sh", Comment: "backup",
	})
	if err != nil {
		t.Fatal(err)
	}

	writes := crontabWrites(cmd)
	if len(writes) != 1 {
		t.Fatalf("expected exactly one crontab write, got %d", len(writes))
	}
	written := writes[0]
	if got := strings.Count(written, "/usr/local/bin/dump.sh"); got != 1 {
		t.Fatalf("dump.sh appears %d times in the written crontab — the hand-written line was duplicated:\n%s", got, written)
	}
	if !strings.Contains(written, exec.CronLabelPrefix+" backup") {
		t.Errorf("written crontab is missing the identity marker label:\n%s", written)
	}
}

func TestCrontabProviderLabelRoundTrip(t *testing.T) {
	// What Set writes must parse back with the same label: List over the
	// written content returns the entry keyed by its Comment.
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewCrontabProvider(cmd)

	err := p.Set(context.Background(), "root", exec.CronEntry{
		Minute: "30", Hour: "4", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		Command: "/usr/bin/sync.sh", Comment: "sync-job",
	})
	if err != nil {
		t.Fatal(err)
	}
	writes := crontabWrites(cmd)
	if len(writes) != 1 {
		t.Fatalf("expected exactly one crontab write, got %d", len(writes))
	}
	wantMarker := "# " + exec.CronLabelPrefix + " sync-job\n30 4 * * * /usr/bin/sync.sh\n"
	if !strings.Contains(writes[0], wantMarker) {
		t.Fatalf("written crontab missing marker+entry block %q:\n%s", wantMarker, writes[0])
	}

	// Feed the written content back through List.
	cmd2 := exectest.NewFakeCommandExec()
	cmd2.SetResult("crontab", &exec.CommandResult{Stdout: wantMarker}, nil)
	p2 := exec.NewCrontabProvider(cmd2)
	entries, err := p2.List(context.Background(), "root")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("List parsed %d entries, want 1", len(entries))
	}
	if entries[0].Comment != "sync-job" || entries[0].Command != "/usr/bin/sync.sh" {
		t.Errorf("round-tripped entry = %+v", entries[0])
	}
}

func TestCrontabProviderRejectsShellMetaUser(t *testing.T) {
	// The write path is a `sh -c` pipeline ending in `crontab -u <user> -`.
	// Before the fix the user name was interpolated UNQUOTED, so a user of
	// `root; touch /tmp/pwned` ran `touch /tmp/pwned` as root alongside the
	// crontab install. The provider must refuse such a name up front — on
	// every entry point — without issuing a single crontab command.
	for _, user := range []string{"root; touch /tmp/pwned", "root && id", "$(id)", "a b", "-u"} {
		cmd := exectest.NewFakeCommandExec()
		p := exec.NewCrontabProvider(cmd)
		entry := exec.CronEntry{Minute: "0", Hour: "2", Command: "/usr/local/bin/dump.sh", Comment: "backup"}

		if err := p.Set(context.Background(), user, entry); err == nil {
			t.Errorf("Set(%q): expected error, got nil", user)
		}
		if _, err := p.List(context.Background(), user); err == nil {
			t.Errorf("List(%q): expected error, got nil", user)
		}
		if err := p.Remove(context.Background(), user, "/usr/local/bin/dump.sh"); err == nil {
			t.Errorf("Remove(%q): expected error, got nil", user)
		}
		if n := cmd.CallCount(); n != 0 {
			t.Errorf("user %q: expected zero crontab invocations, got %d: %+v", user, n, cmd.Calls())
		}
	}
}

func TestCrontabProviderQuotesEveryShellWord(t *testing.T) {
	// Defense in depth: even for an allowlisted user, every word that reaches
	// the shell is single-quoted — the content AND each crontab argument.
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewCrontabProvider(cmd)

	err := p.Set(context.Background(), "deploy", exec.CronEntry{
		Minute: "0", Hour: "2", Command: "/usr/local/bin/dump.sh", Comment: "backup",
	})
	if err != nil {
		t.Fatal(err)
	}
	writes := crontabWrites(cmd)
	if len(writes) != 1 {
		t.Fatalf("expected exactly one crontab write, got %d", len(writes))
	}
	if !strings.HasSuffix(writes[0], "| crontab '-u' 'deploy' '-'") {
		t.Errorf("crontab arguments must each be single-quoted, got:\n%s", writes[0])
	}

	// The current-user form quotes its lone `-` too.
	cmd2 := exectest.NewFakeCommandExec()
	if err := exec.NewCrontabProvider(cmd2).Set(context.Background(), "", exec.CronEntry{
		Minute: "0", Hour: "2", Command: "/usr/local/bin/dump.sh", Comment: "backup",
	}); err != nil {
		t.Fatal(err)
	}
	if w := crontabWrites(cmd2); len(w) != 1 || !strings.HasSuffix(w[0], "| crontab '-'") {
		t.Errorf("current-user write must end in a quoted '-', got %v", w)
	}
}

func TestCrontabProviderRejectsLineInjectingEntry(t *testing.T) {
	// A newline inside any entry field would splice a second, unmanaged job
	// into the crontab; whitespace inside a schedule field shifts the
	// columns. Set must refuse before reading or writing anything.
	bad := []exec.CronEntry{
		{Minute: "0", Hour: "2", Command: "/usr/bin/true\n* * * * * /usr/bin/evil", Comment: "backup"},
		{Minute: "0", Hour: "2", Command: "/usr/bin/true", Comment: "backup\n* * * * * /usr/bin/evil"},
		{Minute: "0 *", Hour: "2", Command: "/usr/bin/true", Comment: "backup"},
		{Minute: "0", Hour: "2\t*", Command: "/usr/bin/true", Comment: "backup"},
	}
	for i, entry := range bad {
		cmd := exectest.NewFakeCommandExec()
		p := exec.NewCrontabProvider(cmd)
		if err := p.Set(context.Background(), "root", entry); err == nil {
			t.Errorf("entry %d: expected Set error, got nil", i)
		}
		if n := cmd.CallCount(); n != 0 {
			t.Errorf("entry %d: expected zero crontab invocations, got %d", i, n)
		}
	}
}
