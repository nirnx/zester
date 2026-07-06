package peeld

import (
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/event"
	"github.com/nirnx/zester/pkg/proto"
)

// publishEvent is the shared event-publish seam for event.send and the
// beacon manager: msgpack-encode the event and publish it on subject via the
// peel's core-NATS PubSub — the events JetStream stream captures it
// server-side (the ScheduledResult pattern), so bustest.FakePubSub works in
// tests. No buffering here: while NATS is unreachable the error propagates
// to the caller (event.send fails the execution; beacons buffer themselves).
func (a *Agent) publishEvent(subject string, ev event.Event) error {
	data, err := bus.Encode(ev)
	if err != nil {
		return fmt.Errorf("peeld: encode event: %w", err)
	}
	if err := a.ps.Publish(subject, data); err != nil {
		return fmt.Errorf("peeld: publish event %s: %w", subject, err)
	}
	return nil
}

// normalizeEventTag canonicalizes an operator-supplied event tag to slash
// form. Normalization rule: a tag containing dots and no slashes is treated
// as the dotted subject form and converted via event.SlashTag
// ("myco.deploy.finished" -> "myco/deploy/finished"); anything else is taken
// verbatim as a slash tag. A tag mixing dots and slashes is consequently
// rejected by event.ValidateTag ('.' is banned inside tag segments), which
// keeps the dots<->slashes subject mapping strictly 1:1.
func normalizeEventTag(raw string) (string, error) {
	tag := raw
	if strings.Contains(tag, ".") && !strings.Contains(tag, "/") {
		tag = event.SlashTag(tag)
	}
	if err := event.ValidateTag(tag); err != nil {
		return "", fmt.Errorf("invalid tag: %w", err)
	}
	return tag, nil
}

// execEventSend implements the "event.send" module (reactor amendment 20):
// publish a custom event on zester.event.<ownPeelID>.send.<dotted-tag>.
//
// Contract:
//   - rawTag is the bare positional / request ID (`zester '<t>' event.send
//     myco/deploy/finished k=v`); slash and dotted forms are both accepted
//     (see normalizeEventTag). Callers without a positional — scheduled
//     runs — pass tag=<tag> in args instead.
//   - The remaining args become Event.Data verbatim. The control keys
//     "test" (dry-run flag) and "tag" (fallback tag source) are stripped.
//   - Depth is stamped from the triggering ExecRequest's ReactorDepth
//     (already parent+1 when a reactor reaction dispatched this job; 0 for
//     organic executions), closing the peel-closed loop counting gap for
//     explicit chains (amendment 21).
//   - test=True reports what would be sent without publishing.
//   - A publish failure (e.g. NATS down) fails the execution — no
//     peel-side buffering for ad-hoc events.
func (a *Agent) execEventSend(rawTag string, args map[string]any, depth int) proto.ExecResponse {
	peelID := a.peelID

	data := make(map[string]any, len(args))
	for k, v := range args {
		data[k] = v
	}
	testMode := isTestArg(data["test"])
	delete(data, "test")
	if rawTag == "" {
		if t, ok := data["tag"].(string); ok && t != "" {
			rawTag = t
		}
		delete(data, "tag")
	}
	if rawTag == "" {
		return proto.ExecResponse{PeelID: peelID, Error: "event.send requires a tag (bare positional or tag=<tag>)"}
	}

	tag, err := normalizeEventTag(rawTag)
	if err != nil {
		return proto.ExecResponse{PeelID: peelID, Error: "event.send: " + err.Error()}
	}
	if len(data) == 0 {
		data = nil
	}

	if testMode {
		return proto.ExecResponse{
			PeelID:  peelID,
			Success: true,
			Test:    true,
			Results: []proto.StateResult{{
				Name:    "event.send",
				Details: map[string]string{"result": "would send event: " + tag},
			}},
		}
	}

	ev := event.NewEvent(tag, data)
	ev.Depth = depth

	subject := bus.PeelEventSendSubject(peelID, event.DottedTag(tag))
	if err := a.publishEvent(subject, ev); err != nil {
		return proto.ExecResponse{PeelID: peelID, Error: "event.send: " + err.Error()}
	}

	a.logger.Info("event sent", "peel", peelID, "tag", tag, "event_id", ev.ID, "depth", ev.Depth)

	return proto.ExecResponse{
		PeelID:  peelID,
		Success: true,
		Results: []proto.StateResult{{
			Name:    "event.send",
			Changed: false,
			Details: map[string]string{
				"result":   "event sent: " + tag,
				"event_id": ev.ID,
			},
		}},
	}
}
