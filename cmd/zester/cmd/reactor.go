package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/reactor"
)

var reactorCmd = &cobra.Command{
	Use:   "reactor",
	Short: "Inspect reactor rules",
	Long: `Inspect the reactor rules loaded by the masters.

The test subcommand dry-runs a match key against the live rule set: a
reactor-enabled master reports which rules match and what their rendered,
validated actions would be — nothing executes.`,
}

var reactorTestCmd = &cobra.Command{
	Use:   "test <match-key>",
	Short: "Dry-run a match key against the loaded reactor rules",
	Long: `Dry-run a match key ("<origin>/<tag>") against the reactor rule set.

A reactor-enabled master matches the key, renders each matched reaction file
with a synthetic event, and reports the normalized, validated actions and
any render errors — WITHOUT executing anything.

Event data for the render comes from --event-json (a full JSON object) and
--data key=value entries; --data overrides --event-json on key collisions.

Examples:
  zester reactor test '_master/enroll/pending/enr-1'
  zester reactor test 'web-01/myco/deploy/finished' --data version=1.2.3
  zester reactor test 'web-01/beacon/web-01/service' --event-json '{"service":"nginx","running":false}'`,
	Args: cobra.ExactArgs(1),
	RunE: runReactorTest,
}

func init() {
	reactorTestCmd.Flags().StringArray("data", nil, "event data entry key=value (repeatable; overrides --event-json)")
	reactorTestCmd.Flags().String("event-json", "", "event data as a JSON object")

	reactorCmd.AddCommand(reactorTestCmd)

	rootCmd.AddCommand(reactorCmd)
}

// reactorTestTimeout bounds one reactor test request/reply round trip.
const reactorTestTimeout = 5 * time.Second

// reactorTestOutput mirrors reactor.TestResponse with json/yaml tags for
// --format json|yaml (the wire struct carries msgpack tags only).
type reactorTestOutput struct {
	Key     string            `json:"key" yaml:"key"`
	Matched []reactorTestRule `json:"matched" yaml:"matched"`
}

type reactorTestRule struct {
	Rule    string   `json:"rule" yaml:"rule"`
	Actions []string `json:"actions,omitempty" yaml:"actions,omitempty"`
	Errors  []string `json:"errors,omitempty" yaml:"errors,omitempty"`
}

func runReactorTest(cmd *cobra.Command, args []string) error {
	key := args[0]

	eventJSON, _ := cmd.Flags().GetString("event-json")
	dataPairs, _ := cmd.Flags().GetStringArray("data")
	data, err := buildReactorTestData(eventJSON, dataPairs)
	if err != nil {
		return err
	}

	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), reactorTestTimeout)
	defer cancel()

	req := reactor.TestRequest{Key: key, Data: data}
	var resp reactor.TestResponse
	if err := client.Request(ctx, bus.SubjectReactorTest, &req, &resp); err != nil {
		if errors.Is(err, nats.ErrNoResponders) {
			return fmt.Errorf("test match key %s: no master with the reactor enabled is answering test requests; ensure at least one zester-master is running with the reactor enabled: %w", key, err)
		}
		return fmt.Errorf("test match key %s: %w", key, err)
	}
	if resp.Err != "" {
		return fmt.Errorf("test match key %s: %s", key, resp.Err)
	}

	format, _ := cmd.Flags().GetString("format")
	switch format {
	case "json":
		out, err := json.MarshalIndent(reactorTestToOutput(key, resp), "", "  ")
		if err != nil {
			return fmt.Errorf("marshal JSON: %w", err)
		}
		fmt.Println(string(out))
	case "yaml":
		out, err := yaml.Marshal(reactorTestToOutput(key, resp))
		if err != nil {
			return fmt.Errorf("marshal YAML: %w", err)
		}
		fmt.Print(string(out))
	default:
		noColor, _ := cmd.Flags().GetBool("no-color")
		useColor := !noColor && shouldColor()
		fmt.Print(formatReactorTestText(key, resp.Matched, useColor))
	}
	return nil
}

// buildReactorTestData builds the synthetic event data for a reactor test:
// eventJSON (a JSON object) is the base, --data key=value entries overlay it.
func buildReactorTestData(eventJSON string, dataPairs []string) (map[string]any, error) {
	var data map[string]any
	if eventJSON != "" {
		if err := json.Unmarshal([]byte(eventJSON), &data); err != nil {
			return nil, fmt.Errorf("parse --event-json: %w", err)
		}
		if data == nil {
			return nil, fmt.Errorf("parse --event-json: expected a JSON object, got null")
		}
	}

	overlay, err := parseEventData(dataPairs)
	if err != nil {
		return nil, err
	}
	if len(overlay) == 0 {
		return data, nil
	}
	if data == nil {
		return overlay, nil
	}
	for k, v := range overlay {
		data[k] = v
	}
	return data, nil
}

// reactorTestToOutput converts the wire response into the json/yaml-tagged
// output shape. Matched is always non-nil so JSON renders [] over null.
func reactorTestToOutput(key string, resp reactor.TestResponse) reactorTestOutput {
	out := reactorTestOutput{Key: key, Matched: make([]reactorTestRule, 0, len(resp.Matched))}
	for _, mr := range resp.Matched {
		out.Matched = append(out.Matched, reactorTestRule{
			Rule:    mr.Rule,
			Actions: mr.Actions,
			Errors:  mr.Errors,
		})
	}
	return out
}

// formatReactorTestText renders the text report: one block per matched rule
// with its action summaries and any render/validation errors.
func formatReactorTestText(key string, matched []reactor.MatchedRule, useColor bool) string {
	var b strings.Builder
	if len(matched) == 0 {
		fmt.Fprintf(&b, "No reactor rules matched key %q.\n", key)
		return b.String()
	}

	fmt.Fprintf(&b, "%d rule(s) matched key %q:\n", len(matched), key)
	for _, mr := range matched {
		color := colorGreen
		if len(mr.Errors) > 0 {
			color = colorRed
		}
		fmt.Fprintf(&b, "\n%s:\n", colorize(mr.Rule, color, useColor))
		for _, action := range mr.Actions {
			fmt.Fprintf(&b, "    - %s\n", action)
		}
		for _, errMsg := range mr.Errors {
			fmt.Fprintf(&b, "    ERROR: %s\n", errMsg)
		}
		if len(mr.Actions) == 0 && len(mr.Errors) == 0 {
			fmt.Fprintf(&b, "    (no actions)\n")
		}
	}
	return b.String()
}
