package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/event"
	"github.com/ptorbus/zester/pkg/reactor"
)

var eventWatchCmd = &cobra.Command{
	Use:   "watch [match-glob]",
	Short: "Watch live events",
	Long: `Watch live events on the zester.event.> namespace until interrupted.

Events print one per line as they arrive. The optional match-glob filters
client-side against the reactor match key "<origin>/<tag>" with the same
fnmatch semantics rules use ('*' crosses '/').

Examples:
  zester event watch
  zester event watch '_master/enroll/pending/*'
  zester event watch '*/beacon/*/service/*'`,
	Args: cobra.MaximumNArgs(1),
	RunE: runEventWatch,
}

// eventWatchRecord is the structured per-event line for --format json/yaml.
type eventWatchRecord struct {
	TS         time.Time      `json:"ts" yaml:"ts"`
	Key        string         `json:"key" yaml:"key"`
	Origin     string         `json:"origin" yaml:"origin"`
	Tag        string         `json:"tag" yaml:"tag"`
	ID         string         `json:"id" yaml:"id"`
	Depth      int            `json:"depth,omitempty" yaml:"depth,omitempty"`
	Provenance string         `json:"provenance,omitempty" yaml:"provenance,omitempty"`
	Data       map[string]any `json:"data,omitempty" yaml:"data,omitempty"`
}

func runEventWatch(cmd *cobra.Command, args []string) error {
	// Compile the optional filter up front so a bad pattern fails fast.
	var re *regexp.Regexp
	if len(args) == 1 {
		var err error
		re, err = reactor.CompileMatchGlob(args[0])
		if err != nil {
			return fmt.Errorf("invalid match glob %q: %w", args[0], err)
		}
	}

	format, _ := cmd.Flags().GetString("format")

	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(context.Background())

	sub, err := client.Subscribe(bus.EventSubjectAll(), func(subject string, data []byte) {
		line, ok := renderEventLine(subject, data, re, format)
		if !ok {
			return // malformed subject or payload: drop
		}
		fmt.Println(line)
	})
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", bus.EventSubjectAll(), err)
	}
	defer sub.Unsubscribe()

	if format == "text" {
		fmt.Fprintf(os.Stderr, "Watching %s (Ctrl-C to stop)\n", bus.EventSubjectAll())
	}

	// Block until interrupted.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	return nil
}

// renderEventLine formats one event message for the watch output. It returns
// ok=false for malformed subjects/payloads (dropped) and for events the
// optional match-key filter excludes. The subject — never the payload — is
// authoritative for origin and tag.
func renderEventLine(subject string, data []byte, re *regexp.Regexp, format string) (string, bool) {
	origin, slashTag, _, err := event.ParseSubject(subject)
	if err != nil {
		return "", false
	}

	var ev event.Event
	if err := bus.Decode(data, &ev); err != nil {
		return "", false
	}

	key := event.MatchKey(origin, slashTag)
	if re != nil && !re.MatchString(key) {
		return "", false
	}

	rec := eventWatchRecord{
		TS:         ev.TS,
		Key:        key,
		Origin:     origin,
		Tag:        slashTag,
		ID:         ev.ID,
		Depth:      ev.Depth,
		Provenance: ev.Origin,
		Data:       ev.Data,
	}

	switch format {
	case "json":
		out, err := json.Marshal(rec)
		if err != nil {
			return "", false
		}
		return string(out), true
	case "yaml":
		out, err := yaml.Marshal(rec)
		if err != nil {
			return "", false
		}
		return "---\n" + strings.TrimSuffix(string(out), "\n"), true
	default:
		return formatEventText(rec), true
	}
}

// formatEventText renders one aligned human-readable event line:
// timestamp, match key, depth (only when >0), compact data.
func formatEventText(rec eventWatchRecord) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %-44s", rec.TS.Local().Format("15:04:05"), rec.Key)
	if rec.Depth > 0 {
		fmt.Fprintf(&b, "  depth=%d", rec.Depth)
	}
	if len(rec.Data) > 0 {
		if out, err := json.Marshal(rec.Data); err == nil {
			fmt.Fprintf(&b, "  %s", out)
		} else {
			fmt.Fprintf(&b, "  %v", rec.Data)
		}
	}
	return strings.TrimRight(b.String(), " ")
}
