package exec

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// crontabHarness fakes crontab(1): -l returns the stored content; the
// printf|crontab - pipe captures the written content back into the store.
type crontabHarness struct {
	content string
	writes  int
}

func (h *crontabHarness) Run(_ context.Context, opts CommandOpts) (*CommandResult, error) {
	if opts.Shell && strings.Contains(opts.Command, "| crontab") {
		h.writes++
		// Extract the single-quoted printf payload.
		start := strings.Index(opts.Command, "'%s' '") + len("'%s' '")
		end := strings.LastIndex(opts.Command, "' | crontab")
		h.content = strings.ReplaceAll(opts.Command[start:end], `'\''`, "'")
		return &CommandResult{ExitCode: 0}, nil
	}
	if len(opts.Args) > 0 && opts.Args[0] == "-l" {
		if h.content == "" {
			return &CommandResult{Stderr: "no crontab for root", ExitCode: 1}, fmt.Errorf("exit status 1")
		}
		return &CommandResult{Stdout: h.content, ExitCode: 0}, nil
	}
	return &CommandResult{ExitCode: 0}, nil
}

const preserveFixture = `MAILTO=ops@example.com
# nightly backup of the db
0 2 * * * /usr/local/bin/db-backup
@reboot /usr/local/bin/warm-cache

PATH=/usr/local/bin:/usr/bin
`

// TestCrontabSetPreservesUnmanagedLines pins the spot-review data-loss
// finding: a Set of an unrelated entry must not destroy environment
// assignments (cron failure mail silently stops), human comments, blank
// lines, or @nickname entries (a JOB deleted).
func TestCrontabSetPreservesUnmanagedLines(t *testing.T) {
	h := &crontabHarness{content: preserveFixture}
	p := NewCrontabProvider(h)

	err := p.Set(context.Background(), "", CronEntry{
		Minute: "5", Hour: "3", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		Command: "/usr/local/bin/new-job", Comment: "new-job",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, must := range []string{
		"MAILTO=ops@example.com",
		"# nightly backup of the db",
		"0 2 * * * /usr/local/bin/db-backup",
		"@reboot /usr/local/bin/warm-cache",
		"PATH=/usr/local/bin:/usr/bin",
		"# " + CronLabelPrefix + " new-job",
		"5 3 * * * /usr/local/bin/new-job",
	} {
		if !strings.Contains(h.content, must) {
			t.Errorf("crontab lost/missing line %q after Set:\n%s", must, h.content)
		}
	}
}

// TestCrontabSetReplacesInPlace: replacing a labeled entry touches only its
// marker+schedule lines, leaving neighbors verbatim.
func TestCrontabSetReplacesInPlace(t *testing.T) {
	h := &crontabHarness{content: "MAILTO=x@y\n# " + CronLabelPrefix + " job\n0 2 * * * /old\n@daily /keep\n"}
	p := NewCrontabProvider(h)

	err := p.Set(context.Background(), "", CronEntry{
		Minute: "1", Hour: "1", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		Command: "/new", Comment: "job",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "MAILTO=x@y\n# " + CronLabelPrefix + " job\n1 1 * * * /new\n@daily /keep\n"
	if h.content != want {
		t.Errorf("in-place replace:\ngot:  %q\nwant: %q", h.content, want)
	}
}

// TestCrontabRemovePreservesUnmanagedLines: removal drops exactly the
// matching entry (and its marker), nothing else.
func TestCrontabRemovePreservesUnmanagedLines(t *testing.T) {
	h := &crontabHarness{content: preserveFixture + "# " + CronLabelPrefix + " doomed\n1 1 * * * /usr/bin/doomed\n"}
	p := NewCrontabProvider(h)

	if err := p.Remove(context.Background(), "", "/usr/bin/doomed"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.content, "doomed") {
		t.Errorf("entry not removed:\n%s", h.content)
	}
	for _, must := range []string{"MAILTO=ops@example.com", "@reboot /usr/local/bin/warm-cache", "# nightly backup of the db"} {
		if !strings.Contains(h.content, must) {
			t.Errorf("crontab lost %q on Remove:\n%s", must, h.content)
		}
	}
}

// TestCrontabRemoveNoMatchDoesNotRewrite: a no-op Remove must not touch the
// crontab at all (cron.absent on a converged host).
func TestCrontabRemoveNoMatchDoesNotRewrite(t *testing.T) {
	h := &crontabHarness{content: preserveFixture}
	p := NewCrontabProvider(h)

	if err := p.Remove(context.Background(), "", "/not/there"); err != nil {
		t.Fatal(err)
	}
	if h.writes != 0 {
		t.Errorf("no-match Remove rewrote the crontab %d times", h.writes)
	}
	if h.content != preserveFixture {
		t.Errorf("content changed on no-op remove")
	}
}
