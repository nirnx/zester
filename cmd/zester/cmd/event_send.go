package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/event"
)

var eventSendCmd = &cobra.Command{
	Use:   "send <tag> [key=value ...]",
	Short: "Send an operator event",
	Long: `Send an operator event into the reactor event stream.

The tag may be given in slash form (myco/deploy/finished) or dotted form
(myco.deploy.finished). Remaining arguments are key=value pairs attached as
the event data. The event publishes on zester.event._admin.send.<tag>, so
reactor rules match it under the "_admin/<tag>" key.

Examples:
  zester event send myco/deploy/finished version=1.2.3
  zester event send maintenance/start reason='kernel patching'`,
	Args: cobra.MinimumNArgs(1),
	RunE: runEventSend,
}

// eventSendRecord is the structured confirmation for --format json/yaml.
type eventSendRecord struct {
	ID       string `json:"id" yaml:"id"`
	Tag      string `json:"tag" yaml:"tag"`
	MatchKey string `json:"match_key" yaml:"match_key"`
	Subject  string `json:"subject" yaml:"subject"`
}

func runEventSend(cmd *cobra.Command, args []string) error {
	slashTag, err := normalizeEventTag(args[0])
	if err != nil {
		return err
	}

	data, err := parseEventData(args[1:])
	if err != nil {
		return err
	}

	ev := event.NewEvent(slashTag, data)
	subject := bus.AdminEventSendSubject(event.DottedTag(slashTag))

	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(context.Background())

	if err := client.Publish(subject, &ev); err != nil {
		return fmt.Errorf("publish event %s: %w", slashTag, err)
	}
	// Publish is buffered; flush so the event provably reached the server
	// before the CLI exits (the events stream captures it server-side).
	if err := client.Flush(); err != nil {
		return fmt.Errorf("flush event %s: %w", slashTag, err)
	}

	rec := eventSendRecord{
		ID:       ev.ID,
		Tag:      slashTag,
		MatchKey: event.MatchKey(bus.OriginAdmin, slashTag),
		Subject:  subject,
	}

	format, _ := cmd.Flags().GetString("format")
	switch format {
	case "json":
		out, err := json.MarshalIndent(rec, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal JSON: %w", err)
		}
		fmt.Println(string(out))
	case "yaml":
		out, err := yaml.Marshal(rec)
		if err != nil {
			return fmt.Errorf("marshal YAML: %w", err)
		}
		fmt.Print(string(out))
	default:
		fmt.Printf("Event %s sent (match key: %s)\n", ev.ID, rec.MatchKey)
	}
	return nil
}
