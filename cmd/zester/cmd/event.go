package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/event"
)

var eventCmd = &cobra.Command{
	Use:   "event",
	Short: "Send and watch reactor events",
	Long: `Send operator events into the reactor event stream and watch live
event traffic.

Operator events publish on the trusted _admin origin
(zester.event._admin.send.<tag>), so reactor rules match them under the
"_admin/<tag>" key. Watching subscribes to the whole zester.event.>
namespace; both require admin credentials carrying the event grants.`,
}

func init() {
	eventCmd.AddCommand(eventSendCmd)
	eventCmd.AddCommand(eventWatchCmd)

	rootCmd.AddCommand(eventCmd)
}

// normalizeEventTag accepts a tag in slash form ("myco/deploy/finished") or
// dotted form ("myco.deploy.finished"), returning the validated slash form.
// A dotted tag is converted only when it contains no slashes — mixed forms
// fail event.ValidateTag with a clear error.
func normalizeEventTag(tag string) (string, error) {
	slashTag := tag
	if !strings.Contains(tag, "/") && strings.Contains(tag, ".") {
		slashTag = event.SlashTag(tag)
	}
	if err := event.ValidateTag(slashTag); err != nil {
		return "", err
	}
	return slashTag, nil
}

// parseEventData parses key=value arguments into an event data map. Unlike
// module-arg parsing (which tolerates positional arguments), every event
// argument must be a key=value pair — silently dropping a typo'd pair into
// an event payload would be worse than failing. Returns nil for no pairs so
// Event.Data stays omitted on the wire.
func parseEventData(pairs []string) (map[string]any, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	data := make(map[string]any, len(pairs))
	for _, pair := range pairs {
		k, v, ok := strings.Cut(pair, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid data argument %q: expected key=value", pair)
		}
		data[k] = v
	}
	return data, nil
}
