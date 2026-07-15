package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/segmentio/ksuid"
	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/cliargs"
	"github.com/nirnx/zester/pkg/enroll"
	"github.com/nirnx/zester/pkg/job"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/moduledoc"
	"github.com/nirnx/zester/pkg/proto"
	"github.com/nirnx/zester/pkg/target"
)

// runExec is the root command's RunE handler for <target> <module.function> [args...].
func runExec(cmd *cobra.Command, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("requires at least 2 arguments: <target> <module.function> [args...]\n\nUsage:\n  zester <target> <module.function> [args...]\n  zester job list\n  zester peel list\n\nExamples:\n  zester '*' test.ping\n  zester 'web-01' cmd.run 'hostname'\n  zester 'web-*' state.apply webserver --timeout 5m\n  zester 'web-01' facts.get os.family --direct")
	}

	tgtExpr := args[0]
	module := args[1]
	remaining := args[2:]

	// Parse module-specific arguments.
	id, modArgs, err := parseModuleArgs(module, remaining)
	if err != nil {
		return fmt.Errorf("parse args: %w", err)
	}

	// --test flag is a convenience for test=True; the peel treats either the
	// same way (dry run).
	if testFlag, _ := cmd.Flags().GetBool("test"); testFlag {
		modArgs["test"] = true
	}

	// Determine timeout.
	timeout := moduleTimeout(cmd, module, modArgs)

	// Determine output format and color.
	format, _ := cmd.Flags().GetString("format")
	useColor := colorEnabled(cmd)
	direct, _ := cmd.Flags().GetBool("direct")

	// Connect to NATS.
	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(context.Background())

	if direct {
		return runDirect(cmd.Context(), client.Conn(), tgtExpr, module, id, modArgs, timeout, format, useColor)
	}
	return runJobMode(cmd.Context(), client, tgtExpr, module, id, modArgs, timeout, format, useColor)
}

// runDirect sends ExecRequests directly to peels via NATS request/reply (no job system).
func runDirect(ctx context.Context, nc *nats.Conn, tgtExpr, module, id string, modArgs map[string]any, timeout time.Duration, format string, useColor bool) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Simple glob resolution for direct mode.
	peelIDs, err := resolveTargetsDirect(ctx, nc, tgtExpr)
	if err != nil {
		return fmt.Errorf("resolve targets: %w", err)
	}
	if len(peelIDs) == 0 {
		return fmt.Errorf("no peels matched target %q", tgtExpr)
	}

	// Send requests concurrently.
	results := make([]directResult, len(peelIDs))
	var wg sync.WaitGroup

	for i, peelID := range peelIDs {
		wg.Add(1)
		go func(idx int, pid string) {
			defer wg.Done()

			req := proto.ExecRequest{
				JID:    ksuid.New().String(),
				Module: module,
				ID:     id,
				Args:   modArgs,
			}

			data, err := bus.Encode(&req)
			if err != nil {
				results[idx] = directResult{peelID: pid, err: fmt.Errorf("encode request: %w", err)}
				return
			}

			msg, err := nc.RequestWithContext(ctx, bus.CmdSubject(pid), data)
			if err != nil {
				results[idx] = directResult{peelID: pid, err: err}
				return
			}

			var resp proto.ExecResponse
			if err := bus.Decode(msg.Data, &resp); err != nil {
				results[idx] = directResult{peelID: pid, err: fmt.Errorf("decode response: %w", err)}
				return
			}

			results[idx] = directResult{peelID: pid, resp: &resp}
		}(i, peelID)
	}

	wg.Wait()

	printDirectResults(results, module, format, useColor)

	// Classified exit: a transport-level error (no responders for a stopped
	// peel, request timeout) is the direct-mode UNREACHABLE class; a reply
	// carrying a failed result is an execution failure.
	failed, unreachable := 0, 0
	for _, r := range results {
		switch {
		case r.err != nil:
			unreachable++
		case r.resp == nil || !r.resp.Success:
			failed++
		}
	}
	return classifyPeelResults(failed, unreachable)
}

// runJobMode dispatches a job through the master and collects returns.
func runJobMode(ctx context.Context, client *bus.Client, tgtExpr, module, id string, modArgs map[string]any, timeout time.Duration, format string, useColor bool) error {
	tt := target.DetectType(tgtExpr)

	resolveCtx, resolveCancel := context.WithTimeout(ctx, 10*time.Second)
	defer resolveCancel()

	// Prefer the master-side resolve service (request/reply against the
	// in-memory facts index); the KV lister remains the fallback so old
	// masters — or none at all — keep working via the facts-bucket scan.
	kvLister := &target.KVPeelLister{JS: client.JetStream()}
	lister := target.NewServiceLister(bus.NewNATSPubSub(client.Conn()), 5*time.Second, kvLister, slog.Default())
	peels, err := target.Resolve(resolveCtx, tgtExpr, tt, lister)
	if err != nil {
		return fmt.Errorf("resolve target %q: %w", tgtExpr, err)
	}

	if len(peels) == 0 {
		fmt.Fprintln(os.Stderr, "No peels matched the target expression.")
		return nil
	}

	if format == "text" {
		fmt.Fprintf(os.Stderr, "Targeting %d peel(s): %v\n", len(peels), displayPeels(peels))
	}

	// Create job. The args map includes the parsed module-specific args.
	j := job.NewJob(module, modArgs, peels, timeout)
	// Carry the bare positional (state ID / conventional "name") so the
	// master forwards it as ExecRequest.ID — same as --direct mode does.
	j.StateID = id
	// Record the operator's raw target expression on the job record for the
	// audit trail (the Targets list only holds the resolved peel IDs).
	j.TargetExpr = tgtExpr
	// Record the operator identity for the audit trail. Note: --direct mode
	// bypasses the job system entirely (no job record is created), so there
	// is nothing to attribute there.
	j.User = currentOperator()

	// Subscribe to return events before dispatching so we don't miss any.
	returnSubject := bus.JobSubject(j.JID) + "." + bus.SubjectJobReturn + ".*"
	returnCh := make(chan job.Return, len(peels))
	returnSub, err := client.Conn().Subscribe(returnSubject, func(msg *nats.Msg) {
		var ret job.Return
		if err := bus.Decode(msg.Data, &ret); err != nil {
			return
		}
		returnCh <- ret
	})
	if err != nil {
		return fmt.Errorf("subscribe job returns: %w", err)
	}
	defer func() { _ = returnSub.Unsubscribe() }()

	// Dispatch to master.
	jobCtx, jobCancel := context.WithTimeout(ctx, timeout)
	defer jobCancel()

	var dispatchResp map[string]string
	if err := client.Request(jobCtx, bus.SubjectDispatch, j, &dispatchResp); err != nil {
		return fmt.Errorf("dispatch job: %w", err)
	}
	if errMsg := dispatchResp["error"]; errMsg != "" {
		return fmt.Errorf("dispatch failed: %s", errMsg)
	}

	if format == "text" {
		fmt.Fprintf(os.Stderr, "Job %s dispatched\n", j.JID)
	}

	// Collect returns in real-time, keyed by peel. A peel can legitimately
	// produce TWO wire returns — its real return and the master's synthetic
	// UNREACHABLE — when the two race (the real one landing just as the fast
	// path fires); counting raw messages would then complete early on a
	// duplicate and miss another target. Dedup by peel and let a real return
	// win over a synthetic one (delivery proof overrides the presence hint).
	seen := make(map[string]job.Return, len(peels))
	order := make([]string, 0, len(peels))
	for {
		select {
		case ret := <-returnCh:
			prev, had := seen[ret.PeelID]
			// First return for this peel, or a real return replacing a
			// synthetic one already shown. Ignore a synthetic arriving after
			// a real return, and any duplicate real return.
			if had && (!prev.Unreachable || ret.Unreachable) {
				continue
			}
			if !had {
				order = append(order, ret.PeelID)
			}
			seen[ret.PeelID] = ret
			// Stream text output as accepted returns arrive.
			if format == "text" {
				printJobReturnText(ret, module, useColor)
			}
			if len(seen) >= len(peels) {
				returns := orderedReturns(seen, order)
				if format != "text" {
					printJobResults(returns, module, format)
				}
				// Classified exit: UNREACHABLE synthetic returns (the
				// master's fast path for heartbeat-absent, never-acking
				// targets) count separately from execution failures.
				return classifyReturns(returns, len(peels))
			}
		case <-jobCtx.Done():
			// Name every missing peel explicitly as UNREACHABLE — the
			// timeout means "no return within the job deadline", which is
			// the same class as the master's fast-path synthetic.
			for _, peelID := range peels {
				if _, ok := seen[peelID]; ok {
					continue
				}
				missing := job.Return{
					PeelID:      peelID,
					Unreachable: true,
					Error:       "no return received within timeout",
				}
				seen[peelID] = missing
				order = append(order, peelID)
				if format == "text" {
					printJobReturnText(missing, module, useColor)
				}
			}
			returns := orderedReturns(seen, order)
			if format != "text" {
				printJobResults(returns, module, format)
			}
			return classifyReturns(returns, len(peels))
		}
	}
}

// orderedReturns flattens the per-peel return map into arrival order.
func orderedReturns(seen map[string]job.Return, order []string) []job.Return {
	returns := make([]job.Return, 0, len(order))
	for _, peelID := range order {
		returns = append(returns, seen[peelID])
	}
	return returns
}

// directResult holds a single peel's response from direct mode.
type directResult struct {
	peelID string
	resp   *proto.ExecResponse
	err    error
}

// resolveTargetsDirect resolves targets for direct mode using simple glob matching
// on the facts KV bucket.
func resolveTargetsDirect(ctx context.Context, nc *nats.Conn, tgtExpr string) ([]string, error) {
	if !isGlob(tgtExpr) {
		// An exact target may be written in the dotted human form; the wire
		// id encodes '.' as '_' (and a dotted id can never exist).
		return []string{enroll.SanitizeGlobDots(tgtExpr)}, nil
	}

	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("create jetstream context: %w", err)
	}

	kv, err := bus.GetBucket(ctx, bus.NewJS(js), bus.BucketFacts)
	if err != nil {
		return nil, fmt.Errorf("get facts bucket: %w", err)
	}

	keys, err := listKVKeys(ctx, kv)
	if err != nil {
		return nil, fmt.Errorf("list fact keys: %w", err)
	}

	// target.GlobMatcher carries the same dotted-hostname normalization as
	// job-mode targeting, so --direct accepts the identical target forms.
	m, err := target.NewGlobMatcher(tgtExpr)
	if err != nil {
		return nil, err
	}
	var matched []string
	for _, k := range keys {
		if m.Match(k, nil) {
			matched = append(matched, k)
		}
	}
	return matched, nil
}

// isGlob returns true if the pattern contains glob metacharacters.
func isGlob(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

// moduleTimeout returns the effective timeout for a command.
// If --timeout was explicitly set by the user, it takes precedence.
// Otherwise, module-specific defaults apply.
func moduleTimeout(cmd *cobra.Command, module string, modArgs map[string]any) time.Duration {
	// Check if the user explicitly set --timeout.
	if cmd.Flags().Changed("timeout") {
		t, _ := cmd.Flags().GetDuration("timeout")
		return t
	}

	switch module {
	case "state.apply":
		// A state.apply without a state reference (state= or the mods= Salt
		// alias) IS a highstate (Salt parity) — give it the highstate budget.
		s, _ := modArgs["state"].(string)
		m, _ := modArgs["mods"].(string)
		if s == "" && m == "" {
			return 10 * time.Minute
		}
		return 5 * time.Minute
	case "state.highstate":
		return 10 * time.Minute
	default:
		return 60 * time.Second
	}
}

// parseModuleArgs parses remaining CLI arguments into an ID and args map based
// on the module.
//
// Resolution order:
//  1. Special-cased query/dispatch modules (facts.*, settings.*, test.ping,
//     state.*) and cmd.run keep their bespoke positional semantics — literal
//     IDs ("ping"/"items"/"keys"/"highstate"), key/value positionals, or a
//     synthetic "ad-hoc" ID. None are self-documenting state modules, so they
//     are absent from the embedded docdata.
//  2. Self-documenting modules (present in pkg/moduledoc's embedded docdata)
//     bind the bare positional to their DECLARED primary parameter — this
//     replaces the former hand-maintained per-module positional table.
//  3. Everything else falls through to the generic first-arg-is-ID fallback
//     (mixed-fleet honesty: a module not yet migrated has no known primary, so
//     the CLI does not guess one).
func parseModuleArgs(module string, remaining []string) (string, map[string]any, error) {
	args := make(map[string]any)

	switch module {
	case "cmd.run":
		if len(remaining) == 0 {
			return "", nil, fmt.Errorf("cmd.run requires a command argument")
		}
		// Salt-style key=value invocations must not become the literal command
		// (P1 regression: `cmd.run name=echo hi` executed the command
		// "name=echo hi", exit 127). When the FIRST token is an explicit
		// command=/cmd=/name= assignment, the whole invocation is key=value
		// form; otherwise the first token is the positional command, verbatim
		// (`cmd.run 'FOO=bar env'` still works — FOO is not a command key).
		if k, _, ok := strings.Cut(remaining[0], "="); ok && (k == "command" || k == "cmd" || k == "name") {
			cliargs.ParseKeyValues(remaining, args)
			return "ad-hoc", args, nil
		}
		args["command"] = remaining[0]
		cliargs.ParseKeyValues(remaining[1:], args)
		return "ad-hoc", args, nil

	case "test.ping":
		cliargs.ParseKeyValues(remaining, args)
		return "ping", args, nil

	case "facts.items":
		cliargs.ParseKeyValues(remaining, args)
		return "items", args, nil

	case "facts.get":
		if len(remaining) == 0 {
			return "", nil, fmt.Errorf("facts.get requires a key argument")
		}
		args["key"] = remaining[0]
		cliargs.ParseKeyValues(remaining[1:], args)
		return remaining[0], args, nil

	case "facts.keys":
		cliargs.ParseKeyValues(remaining, args)
		return "keys", args, nil

	case "facts.set":
		if len(remaining) < 2 {
			return "", nil, fmt.Errorf("facts.set requires a key and value argument")
		}
		args["key"] = remaining[0]
		args["value"] = remaining[1]
		cliargs.ParseKeyValues(remaining[2:], args)
		return remaining[0], args, nil

	case "settings.items", "pillar.items":
		cliargs.ParseKeyValues(remaining, args)
		return "items", args, nil

	case "settings.get", "pillar.get":
		// pillar.* is the peel's Salt-compat alias for settings.* — the same
		// handler answers both and reads only args["key"], so the keyed form
		// must bind here for both spellings (review round 5 critic: pillar.get
		// previously fell through to the generic fallback, leaving args empty
		// and the peel erroring "requires a key argument").
		if len(remaining) == 0 {
			return "", nil, fmt.Errorf("%s requires a key argument", module)
		}
		args["key"] = remaining[0]
		cliargs.ParseKeyValues(remaining[1:], args)
		return remaining[0], args, nil

	case "settings.keys", "pillar.keys":
		cliargs.ParseKeyValues(remaining, args)
		return "keys", args, nil

	case "state.apply":
		// The key=value form is recognized ONLY via the DECLARED keys —
		// state=, mods= (Salt's kwarg), test= — mirroring cmd.run's
		// command=/cmd=/name= guard. Any other first token (including an
		// arbitrary assignment like a staet= typo or a misordered env=prod)
		// stays a positional state name and fails loudly at compile time on
		// the peel, rather than silently escalating to a full highstate.
		if len(remaining) > 0 {
			if k, _, ok := strings.Cut(remaining[0], "="); !ok || (k != "state" && k != "mods" && k != "test") {
				state := remaining[0]
				args["state"] = state
				cliargs.ParseKeyValues(remaining[1:], args)
				return state, args, nil
			}
		}
		// Key=value (or bare) form. Stray positionals are rejected outright:
		// ParseKeyValues would silently drop them, and a dropped state name
		// must never turn into a fleet-wide highstate.
		for _, tok := range remaining {
			if !strings.Contains(tok, "=") {
				return "", nil, fmt.Errorf("state.apply: unexpected positional %q after key=value arguments (use state=%s, or put the state name first)", tok, tok)
			}
		}
		cliargs.ParseKeyValues(remaining, args)
		if s, _ := args["state"].(string); s != "" {
			return s, args, nil
		}
		if m, _ := args["mods"].(string); m != "" {
			return m, args, nil
		}
		// Salt parity: `state.apply` with no state reference runs the full
		// highstate (`salt '*' state.apply` semantics); the peel-side
		// dispatcher performs the same rewrite for non-CLI producers.
		return "highstate", args, nil

	case "state.highstate":
		cliargs.ParseKeyValues(remaining, args)
		return "highstate", args, nil
	}

	// Self-documenting modules describe their own primary parameter in the
	// embedded docdata. file.managed and pkg.installed were formerly hard-coded
	// here; they now resolve through this path (pkg.installed's primary is
	// "name" — byte-identical to the old table; file.managed's primary is its
	// canonical "name", with "path" a registered alias, so it is decode-
	// identical on any migrated peel and the ID still carries the value).
	if id, a, ok, err := docdataPositional(module, remaining); ok {
		return id, a, err
	}

	// Generic fallback: first arg is the ID, rest are key=value.
	id := "ad-hoc"
	if len(remaining) > 0 {
		id = remaining[0]
		cliargs.ParseKeyValues(remaining[1:], args)
	}
	return id, args, nil
}

// docdataPositional binds a bare CLI positional to a self-documenting module's
// declared primary parameter, looked up from the embedded docdata. ok is false
// (leave it to the generic fallback) when the module is not in docdata, has no
// primary field, or its primary carries a default (nothing to require). When ok
// is true, the positional is stored under the primary's canonical name AND
// returned as the ExecRequest ID — same as a state ID — and a missing positional
// for a default-less primary is a usage error.
func docdataPositional(module string, remaining []string) (id string, args map[string]any, ok bool, err error) {
	mi, found := moduledoc.Lookup(module)
	if !found {
		return "", nil, false, nil
	}
	primary, found := primaryParam(mi)
	if !found {
		return "", nil, false, nil
	}
	if len(remaining) == 0 {
		if primary.HasDefault {
			// The primary can be omitted; let the generic fallback apply.
			return "", nil, false, nil
		}
		if mi.Kind == modschema.KindExec && !primary.Required {
			// An execution function with an OPTIONAL primary supports bare
			// invocation: dispatch with an empty ID and no primary arg so the
			// function sees an absent name (e.g. bare `sys.doc` returns the
			// unified index; the state-module rule below does not apply since
			// there is no state ID to stand in).
			return "", map[string]any{}, true, nil
		}
		return "", nil, true, fmt.Errorf("%s requires a %s argument", module, primary.Name)
	}
	// Key=value guard (round-5 critic; generalizes the round-4 cmd.run fix):
	// when the FIRST token is an explicit assignment to one of the module's
	// DECLARED parameter keys (canonical name or alias), the whole invocation
	// is key=value form — binding it verbatim to the primary would install a
	// package literally named "name=nginx". An assignment to an undeclared key
	// stays a positional value (only the schema's own key set switches modes,
	// so exotic positional values containing '=' keep working).
	if k, _, cut := strings.Cut(remaining[0], "="); cut && declaredParamKey(mi, k) {
		args = make(map[string]any)
		cliargs.ParseKeyValues(remaining, args)
		id, found := primaryValue(args, primary)
		if !found {
			if primary.HasDefault {
				return "ad-hoc", args, true, nil
			}
			if mi.Kind == modschema.KindExec && !primary.Required {
				return "", args, true, nil
			}
			return "", nil, true, fmt.Errorf("%s requires a %s argument", module, primary.Name)
		}
		return id, args, true, nil
	}
	args = make(map[string]any)
	args[primary.Name] = remaining[0]
	cliargs.ParseKeyValues(remaining[1:], args)
	return remaining[0], args, true, nil
}

// declaredParamKey reports whether key is a declared parameter key of the
// module — a canonical name or a registered alias.
func declaredParamKey(mi modschema.ModuleInfo, key string) bool {
	for _, f := range mi.Params {
		if f.Name == key {
			return true
		}
		for _, a := range f.Aliases {
			if a == key {
				return true
			}
		}
	}
	return false
}

// primaryValue resolves the primary parameter's string value from parsed
// key=value args, canonical name first then aliases in declaration order,
// skipping empty strings (the framework's absence sentinel).
func primaryValue(args map[string]any, primary modschema.Field) (string, bool) {
	keys := append([]string{primary.Name}, primary.Aliases...)
	for _, k := range keys {
		if v, ok := args[k].(string); ok && v != "" {
			return v, true
		}
	}
	return "", false
}

// primaryParam returns the module's primary parameter field, if it has one.
func primaryParam(mi modschema.ModuleInfo) (modschema.Field, bool) {
	for _, f := range mi.Params {
		if f.Primary {
			return f, true
		}
	}
	return modschema.Field{}, false
}
