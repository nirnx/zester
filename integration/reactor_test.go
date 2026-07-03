//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tcexec "github.com/testcontainers/testcontainers-go/exec"
)

// ---------------------------------------------------------------------------
// Reactor end-to-end tests.
//
// The playground bakes reactor rules into /data/reactor (playground/reactor,
// copied by playground-init, published by the publisher-lease-holding master
// to the reactor-files bucket, hot-loaded by every master):
//
//	'_admin/itest/ping/*'                    -> reactor.itest_echo
//	'_admin/itest/chain/start/*'             -> reactor.itest_chain_start
//	'_master/reaction/itest/chain/next/*'    -> reactor.itest_chain_next
//	'web-01/beacon/*/service'                -> reactor.heal_service
// ---------------------------------------------------------------------------

// reactorExpectedRules must match the number of rule refs in
// playground/reactor/top.zy.
const reactorExpectedRules = 4

// reactorSettleWindow is how long the absence tests wait before asserting
// that no reaction job appeared. The live pipeline reacts in well under a
// second, so 10s is a generous bound; a sleep (not a poll) is inherent to
// asserting absence.
const reactorSettleWindow = 10 * time.Second

// tryExecInContainer runs a command in a compose service and returns its
// combined output and exit code WITHOUT failing the test — for use inside
// poll loops where transient failures should just retry.
func tryExecInContainer(ctx context.Context, service string, cmd []string) (string, int, error) {
	container, err := stack.ServiceContainer(ctx, service)
	if err != nil {
		return "", -1, fmt.Errorf("get container %s: %w", service, err)
	}
	exitCode, reader, err := container.Exec(ctx, cmd, tcexec.Multiplexed())
	if err != nil {
		return "", -1, fmt.Errorf("exec in %s: %w", service, err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		return "", exitCode, fmt.Errorf("read exec output from %s: %w", service, err)
	}
	return string(output), exitCode, nil
}

// extractJSONObject extracts the outermost JSON object from output that may
// carry stderr noise or trailing text (e.g. `zester job show` appends a
// plain-text Returns table after the JSON record).
func extractJSONObject(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start == -1 || end == -1 || end < start {
		return s
	}
	return s[start : end+1]
}

// waitForCondition polls cond until it returns true or the timeout expires
// (Fatal). Poll-based synchronization — never a bare sleep — for anything
// that eventually becomes observable.
func waitForCondition(t *testing.T, timeout, interval time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(interval)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// uniqueRunID returns a tag-charset-safe unique suffix so every test run
// uses fresh event tags (rules match on globs, so uniqueness isolates runs).
func uniqueRunID(prefix string) string {
	return prefix + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// reactorReadyOnce gates all reactor tests on both masters having loaded the
// playground rule set and reporting a healthy reactor readiness check.
var (
	reactorReadyOnce sync.Once
	reactorReadyErr  error
)

// waitForReactorReady blocks until every master (a) reports
// zester_reactor_rules_loaded == reactorExpectedRules on /metrics and
// (b) reports the 'reactor' readiness check OK on /readyz. Both matter:
// events are consumed by exactly ONE master (shared durable consumer), so
// whichever master processes a test event must hold the rules.
func waitForReactorReady(t *testing.T) {
	t.Helper()
	reactorReadyOnce.Do(func() { reactorReadyErr = pollReactorReady() })
	if reactorReadyErr != nil {
		t.Fatalf("reactor not ready: %v", reactorReadyErr)
	}
}

func pollReactorReady() error {
	ctx := context.Background()
	masters := []string{"master"}
	if hasService("master-2") {
		masters = append(masters, "master-2")
	}
	wantGauge := fmt.Sprintf("zester_reactor_rules_loaded %d", reactorExpectedRules)

	deadline := time.Now().Add(3 * time.Minute)
	var lastState string
	for time.Now().Before(deadline) {
		ready := true
		for _, svc := range masters {
			metrics, code, err := tryExecInContainer(ctx, svc, []string{
				"sh", "-lc", "curl -fsS http://127.0.0.1:9091/metrics",
			})
			if err != nil || code != 0 || !strings.Contains(metrics, wantGauge) {
				lastState = fmt.Sprintf("%s: rules gauge not at %d yet", svc, reactorExpectedRules)
				ready = false
				break
			}
			// /readyz may be 503 overall (unrelated checks); read the body
			// regardless and inspect only the reactor check.
			readyz, _, err := tryExecInContainer(ctx, svc, []string{
				"sh", "-lc", "curl -sS http://127.0.0.1:9091/readyz",
			})
			if err != nil || !reactorCheckOK(readyz) {
				lastState = fmt.Sprintf("%s: reactor readiness check not ok (body=%q)", svc, strings.TrimSpace(readyz))
				ready = false
				break
			}
		}
		if ready {
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("timed out after 3m; last state: %s", lastState)
}

// reactorCheckOK reports whether a /readyz JSON body carries
// checks.reactor.status == "ok".
func reactorCheckOK(body string) bool {
	var parsed struct {
		Checks map[string]struct {
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(body)), &parsed); err != nil {
		return false
	}
	c, ok := parsed.Checks["reactor"]
	return ok && c.Status == "ok"
}

// sendAdminEvent publishes an operator event from the admin container
// (zester event send -> zester.event._admin.send.<tag>) and returns the
// event ID the CLI minted — the reaction jobs' metadata.event_id anchor.
func sendAdminEvent(t *testing.T, tag string, dataPairs ...string) string {
	t.Helper()
	cmd := append([]string{"zester", "--format", "json", "--no-color", "event", "send", tag}, dataPairs...)
	out := execInContainer(t, "admin", cmd)

	var rec struct {
		ID       string `json:"id"`
		MatchKey string `json:"match_key"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(out)), &rec); err != nil {
		t.Fatalf("parse event send output: %v\nraw: %s", err, out)
	}
	if rec.ID == "" {
		t.Fatalf("event send returned no event ID: %s", out)
	}
	t.Logf("sent event %s (match key %s)", rec.ID, rec.MatchKey)
	return rec.ID
}

// listReactionJobs returns jid -> user for every reaction job (rxn- JID
// prefix) visible in `zester job list`. Tolerant of transient CLI failures
// (returns an empty map) so it is safe inside poll loops.
func listReactionJobs() map[string]string {
	out, code, err := tryExecInContainer(context.Background(), "admin", []string{"zester", "job", "list"})
	if err != nil || code != 0 {
		return map[string]string{}
	}
	jobs := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "rxn-") {
			continue
		}
		user := ""
		for _, f := range fields {
			if strings.HasPrefix(f, "reactor:") {
				user = f
				break
			}
		}
		jobs[fields[0]] = user
	}
	return jobs
}

// countReactionJobs counts reaction jobs, optionally filtered by the exact
// Job.User attribution ("" counts all rxn- jobs).
func countReactionJobs(user string) int {
	n := 0
	for _, u := range listReactionJobs() {
		if user == "" || u == user {
			n++
		}
	}
	return n
}

// tryJobShow fetches one job record as parsed JSON (the msgpack job record
// re-encoded by `zester job show`). Returns an error instead of failing so
// poll loops can retry.
func tryJobShow(jid string) (map[string]any, error) {
	out, code, err := tryExecInContainer(context.Background(), "admin", []string{"zester", "job", "show", jid})
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("job show %s exited %d: %s", jid, code, out)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(extractJSONObject(out)), &rec); err != nil {
		return nil, fmt.Errorf("parse job show %s: %w (raw=%q)", jid, err, out)
	}
	return rec, nil
}

// jobMetadata extracts the metadata map from a job record.
func jobMetadata(rec map[string]any) map[string]any {
	md, _ := rec["metadata"].(map[string]any)
	if md == nil {
		return map[string]any{}
	}
	return md
}

// jsonNumber coerces a JSON-decoded numeric value to float64.
func jsonNumber(v any) float64 {
	f, _ := v.(float64)
	return f
}

// startEventWatch launches `zester event watch <glob>` in the admin
// container in the background (bounded by a busybox timeout so it can never
// outlive the suite), then blocks until the subscription banner appears on
// stderr — an event sent afterwards is provably observable. Returns the
// path of the stdout capture file inside the admin container.
func startEventWatch(t *testing.T, glob, name string, bound time.Duration) string {
	t.Helper()
	outFile := "/tmp/" + name + ".out"
	errFile := "/tmp/" + name + ".err"

	go func() {
		cmd := fmt.Sprintf("rm -f %s %s; timeout %d zester event watch '%s' >%s 2>%s",
			outFile, errFile, int(bound.Seconds()), glob, outFile, errFile)
		// Exit status intentionally ignored: busybox timeout terminates the
		// watcher with a non-zero status when the bound expires.
		_, _, _ = tryExecInContainer(context.Background(), "admin", []string{"sh", "-c", cmd})
	}()
	t.Cleanup(func() {
		_, _, _ = tryExecInContainer(context.Background(), "admin", []string{
			"sh", "-c", fmt.Sprintf("pkill -f 'zester event watch' 2>/dev/null; rm -f %s %s; true", outFile, errFile),
		})
	})

	waitForCondition(t, 60*time.Second, time.Second, "event watch subscription for "+glob, func() bool {
		out, _, err := tryExecInContainer(context.Background(), "admin", []string{
			"sh", "-c", "cat " + errFile + " 2>/dev/null || true",
		})
		return err == nil && strings.Contains(out, "Watching")
	})
	return outFile
}

// waitForWatchLine polls the watch capture file until it contains want.
func waitForWatchLine(t *testing.T, outFile, want, what string, timeout time.Duration) {
	t.Helper()
	waitForCondition(t, timeout, time.Second, what, func() bool {
		out, _, err := tryExecInContainer(context.Background(), "admin", []string{
			"sh", "-c", "cat " + outFile + " 2>/dev/null || true",
		})
		return err == nil && strings.Contains(out, want)
	})
}

// ---------------------------------------------------------------------------
// 1. Event -> reaction: an _admin event dispatches a job at web-01 with the
// event data interpolated, attributed to the rule, deduped on a rxn- JID.
// ---------------------------------------------------------------------------

func TestReactor_EventTriggersReaction(t *testing.T) {
	waitForReactorReady(t)

	runID := uniqueRunID("ping")
	tag := "itest/ping/" + runID
	msg := "hello-" + runID
	eventID := sendAdminEvent(t, tag, "msg="+msg)

	// Poll job list for the reaction job carrying OUR event ID (the rule may
	// have fired for earlier runs too; metadata.event_id uniquifies).
	const wantUser = "reactor:reactor.itest_echo"
	var jid string
	var rec map[string]any
	waitForCondition(t, 2*time.Minute, 2*time.Second, "reaction job for event "+eventID, func() bool {
		for candidate, user := range listReactionJobs() {
			if user != wantUser {
				continue
			}
			r, err := tryJobShow(candidate)
			if err != nil {
				continue
			}
			if jobMetadata(r)["event_id"] != eventID {
				continue
			}
			jid, rec = candidate, r
			return true
		}
		return false
	})

	// Deterministic content-addressed JID.
	if !strings.HasPrefix(jid, "rxn-") {
		t.Errorf("reaction JID = %q, want rxn- prefix", jid)
	}
	// Attribution: Job.User is the rule-qualified reactor identity.
	if got, _ := rec["user"].(string); got != wantUser {
		t.Errorf("job user = %q, want %q", got, wantUser)
	}
	// Provenance metadata.
	md := jobMetadata(rec)
	if got := md["source"]; got != "reactor" {
		t.Errorf("metadata.source = %v, want reactor", got)
	}
	if got := md["rule"]; got != "reactor.itest_echo" {
		t.Errorf("metadata.rule = %v, want reactor.itest_echo", got)
	}
	if got := md["event_tag"]; got != tag {
		t.Errorf("metadata.event_tag = %v, want %s", got, tag)
	}
	// Data interpolation: the reaction template rendered event.data.msg into
	// the bare positional (state_id).
	if got, _ := rec["state_id"].(string); got != msg {
		t.Errorf("state_id = %q, want %q (event data interpolation)", got, msg)
	}
	// Targeting: the rule dispatches at web-01 exactly.
	if tgts, _ := rec["targets"].([]any); len(tgts) != 1 || tgts[0] != "web-01" {
		t.Errorf("targets = %v, want [web-01]", rec["targets"])
	}

	// The target peel must execute the job and return success.
	waitForCondition(t, 2*time.Minute, 2*time.Second, "reaction job "+jid+" to complete successfully", func() bool {
		r, err := tryJobShow(jid)
		if err != nil {
			return false
		}
		status, _ := r["status"].(string)
		return status == "complete" && jsonNumber(r["success_count"]) >= 1
	})

	// The per-peel return is visible in job show's Returns table.
	out := execInContainer(t, "admin", []string{"zester", "job", "show", jid})
	if !strings.Contains(out, "web-01") || !strings.Contains(out, "true") {
		t.Errorf("job show returns missing successful web-01 entry:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// 2. Dry run: `zester reactor test` reports the matched rule and rendered
// actions but dispatches NOTHING.
// ---------------------------------------------------------------------------

func TestReactor_TestDryRunDispatchesNothing(t *testing.T) {
	waitForReactorReady(t)

	before := countReactionJobs("")

	out := execInContainer(t, "admin", []string{
		"zester", "--format", "json", "--no-color",
		"reactor", "test", "_admin/itest/ping/dryrun", "--data", "msg=dry-run-probe",
	})
	var resp struct {
		Key     string `json:"key"`
		Matched []struct {
			Rule    string   `json:"rule"`
			Actions []string `json:"actions"`
			Errors  []string `json:"errors"`
		} `json:"matched"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(out)), &resp); err != nil {
		t.Fatalf("parse reactor test output: %v\nraw: %s", err, out)
	}

	if len(resp.Matched) != 1 {
		t.Fatalf("matched %d rules, want 1: %s", len(resp.Matched), out)
	}
	mr := resp.Matched[0]
	if mr.Rule != "reactor.itest_echo" {
		t.Errorf("matched rule = %q, want reactor.itest_echo", mr.Rule)
	}
	if len(mr.Errors) != 0 {
		t.Errorf("dry run reported errors: %v", mr.Errors)
	}
	if len(mr.Actions) != 1 {
		t.Fatalf("dry run reported %d actions, want 1: %v", len(mr.Actions), mr.Actions)
	}
	// The rendered action summary carries the synthetic event data.
	action := mr.Actions[0]
	for _, want := range []string{"dispatch test.echo", `target="web-01"`, `state_id="dry-run-probe"`} {
		if !strings.Contains(action, want) {
			t.Errorf("action summary %q missing %q", action, want)
		}
	}

	// Absence check: nothing may have been dispatched. The pipeline reacts
	// sub-second when live, so a fixed settle window bounds the wait.
	time.Sleep(reactorSettleWindow)
	if after := countReactionJobs(""); after != before {
		t.Errorf("reactor test dispatched jobs: rxn- count %d -> %d", before, after)
	}
}

// ---------------------------------------------------------------------------
// 3. Chaining + depth cap: chain/start emits a derived event; the derived
// rule re-emits the same tag (self-chain). With max_chain_depth=3 exactly
// two hops run jobs (derived events at depth 1 and 2) and the depth-3
// emission is refused — the chain provably stops.
// ---------------------------------------------------------------------------

func TestReactor_ChainStopsAtDepthCap(t *testing.T) {
	waitForReactorReady(t)

	const hopUser = "reactor:reactor.itest_chain_next"
	const startUser = "reactor:reactor.itest_chain_start"
	runID := uniqueRunID("chain")

	before := countReactionJobs(hopUser)

	sendAdminEvent(t, "itest/chain/start/"+runID, "run="+runID)

	// Depth math: admin event (depth 0) -> start rule emits derived event at
	// depth 1 -> hop rule dispatches job #1 and emits depth 2 -> hop rule
	// dispatches job #2 and REFUSES the depth-3 emission (cap 3). A broken
	// depth guard would keep the chain (and the job count) growing.
	waitForCondition(t, 3*time.Minute, 2*time.Second, "two chain hop reaction jobs", func() bool {
		return countReactionJobs(hopUser)-before >= 2
	})

	// Prove the chain STOPPED: no further hop job may appear. 15s covers
	// several full hop latencies (each hop completes in well under a second).
	time.Sleep(15 * time.Second)
	if got := countReactionJobs(hopUser) - before; got != 2 {
		t.Fatalf("chain produced %d hop jobs, want exactly 2 (depth cap must stop the self-chain)", got)
	}

	// The two hops rendered depths 1 and 2 into their state_ids.
	wantStateIDs := map[string]bool{
		"chain-" + runID + "-depth-1": false,
		"chain-" + runID + "-depth-2": false,
	}
	for jid, user := range listReactionJobs() {
		if user != hopUser {
			continue
		}
		rec, err := tryJobShow(jid)
		if err != nil {
			t.Fatalf("job show %s: %v", jid, err)
		}
		sid, _ := rec["state_id"].(string)
		if seen, ok := wantStateIDs[sid]; ok {
			if seen {
				t.Errorf("duplicate hop job for state_id %q (dedup broken?)", sid)
			}
			wantStateIDs[sid] = true
			if got := jobMetadata(rec)["source"]; got != "reactor" {
				t.Errorf("hop job %s metadata.source = %v, want reactor", jid, got)
			}
		}
	}
	for sid, seen := range wantStateIDs {
		if !seen {
			t.Errorf("no hop job found with state_id %q", sid)
		}
	}

	// The start rule only emits — it must never dispatch jobs.
	if n := countReactionJobs(startUser); n != 0 {
		t.Errorf("chain start rule dispatched %d jobs, want 0 (event.send only)", n)
	}
}

// ---------------------------------------------------------------------------
// 4. Event watch: a live `zester event watch` session in the admin container
// prints a matching event as it arrives.
// ---------------------------------------------------------------------------

func TestReactor_EventWatch(t *testing.T) {
	waitForReactorReady(t)

	runID := uniqueRunID("watch")
	tag := "itest/watch/" + runID

	outFile := startEventWatch(t, "_admin/itest/watch/*", "itest-watch-"+runID, 3*time.Minute)

	sendAdminEvent(t, tag, "msg=watch-hello")

	// The text format prints the match key "<origin>/<tag>" per event line.
	waitForWatchLine(t, outFile, "_admin/"+tag, "watched event line for "+tag, 60*time.Second)
}

// ---------------------------------------------------------------------------
// 5. Unmatched event: an event no rule matches must produce no reaction job.
// ---------------------------------------------------------------------------

func TestReactor_UnmatchedEventNoReaction(t *testing.T) {
	waitForReactorReady(t)

	runID := uniqueRunID("nomatch")
	tag := "itest/nomatch/" + runID

	// The dry run confirms the key matches zero rules.
	out := execInContainer(t, "admin", []string{
		"zester", "--format", "json", "--no-color", "reactor", "test", "_admin/" + tag,
	})
	var resp struct {
		Matched []any `json:"matched"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(out)), &resp); err != nil {
		t.Fatalf("parse reactor test output: %v\nraw: %s", err, out)
	}
	if len(resp.Matched) != 0 {
		t.Fatalf("expected no matched rules for %s, got: %s", tag, out)
	}

	before := countReactionJobs("")
	sendAdminEvent(t, tag, "msg=nobody-listens")

	// Absence check (see reactorSettleWindow).
	time.Sleep(reactorSettleWindow)
	if after := countReactionJobs(""); after != before {
		t.Errorf("unmatched event produced reaction jobs: rxn- count %d -> %d", before, after)
	}
}

// ---------------------------------------------------------------------------
// 6. Beacon self-heal: web-01 runs the v1 service beacon (settings-driven,
// playground/settings/webservers/beacons.zy) polling nginx every 2s. The
// 'web-01/beacon/*/service' rule starts the service back up whenever a
// "running: false" transition event arrives — the full peel-beacon ->
// event -> reactor -> job -> peel loop.
// ---------------------------------------------------------------------------

func TestReactor_BeaconServiceSelfHeal(t *testing.T) {
	waitForReactorReady(t)

	// Observe web-01's beacon events so every phase synchronizes on an
	// actual observed transition instead of a timer.
	runID := uniqueRunID("heal")
	outFile := startEventWatch(t, "web-01/beacon/*", "itest-beacon-"+runID, 5*time.Minute)

	nginxState := func() string {
		out, _, err := tryExecInContainer(context.Background(), "web-01", []string{
			"sh", "-c", "systemctl is-active nginx || true",
		})
		if err != nil {
			return ""
		}
		return strings.TrimSpace(out)
	}

	// Phase 1: make sure nginx is RUNNING and the beacon has observed it
	// (the emitted running:true transition proves the beacon's baseline).
	// The peel image never starts nginx on web-01, so the beacon's boot
	// baseline is "stopped" and this start is a guaranteed transition.
	if state := nginxState(); state != "active" {
		execInContainer(t, "web-01", []string{"sh", "-c", "systemctl start nginx"})
	}
	waitForWatchLine(t, outFile, `"running":true`, "beacon to observe nginx running", 90*time.Second)

	// Phase 2: kill nginx OUTSIDE zester (plain container exec) so the peel
	// is idle and the beacon polls freely.
	execInContainer(t, "web-01", []string{"sh", "-c", "systemctl stop nginx"})
	waitForWatchLine(t, outFile, `"running":false`, "beacon to report nginx down", 90*time.Second)

	// Phase 3: the heal_service reaction must bring nginx back.
	waitForCondition(t, 2*time.Minute, 2*time.Second, "reactor to self-heal nginx on web-01", func() bool {
		return nginxState() == "active"
	})

	// The heal ran as an attributed reaction job.
	const healUser = "reactor:reactor.heal_service"
	waitForCondition(t, 60*time.Second, 2*time.Second, "heal_service reaction job in job list", func() bool {
		return countReactionJobs(healUser) >= 1
	})

	// Verify the heal job's provenance and targeting.
	for jid, user := range listReactionJobs() {
		if user != healUser {
			continue
		}
		rec, err := tryJobShow(jid)
		if err != nil {
			t.Fatalf("job show %s: %v", jid, err)
		}
		md := jobMetadata(rec)
		if got := md["source"]; got != "reactor" {
			t.Errorf("heal job %s metadata.source = %v, want reactor", jid, got)
		}
		if got := md["event_tag"]; got != "beacon/web-01/service" {
			t.Errorf("heal job %s metadata.event_tag = %v, want beacon/web-01/service", jid, got)
		}
		if got, _ := rec["function"].(string); got != "service.running" {
			t.Errorf("heal job %s function = %q, want service.running", jid, got)
		}
		if tgts, _ := rec["targets"].([]any); len(tgts) != 1 || tgts[0] != "web-01" {
			t.Errorf("heal job %s targets = %v, want [web-01]", jid, rec["targets"])
		}
		break
	}
}
