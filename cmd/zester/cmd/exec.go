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
	timeout := moduleTimeout(cmd, module)

	// Determine output format and color.
	format, _ := cmd.Flags().GetString("format")
	noColor, _ := cmd.Flags().GetBool("no-color")
	useColor := !noColor && shouldColor()
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

	// Output results.
	hasError := false
	for _, r := range results {
		if r.err != nil || (r.resp != nil && !r.resp.Success) {
			hasError = true
			break
		}
	}

	printDirectResults(results, module, format, useColor)

	if hasError {
		return fmt.Errorf("one or more peels returned errors")
	}
	return nil
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
	defer returnSub.Unsubscribe()

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

	// Collect returns in real-time.
	var returns []job.Return
	received := 0
	for {
		select {
		case ret := <-returnCh:
			received++
			returns = append(returns, ret)
			// Stream text output as returns arrive.
			if format == "text" {
				printJobReturnText(ret, module, useColor)
			}
			if received >= len(peels) {
				if format != "text" {
					printJobResults(returns, module, format)
				}
				return nil
			}
		case <-jobCtx.Done():
			if format == "text" {
				fmt.Fprintf(os.Stderr, "\nTimeout: received %d/%d returns for job %s\n", received, len(peels), j.JID)
			}
			if format != "text" && len(returns) > 0 {
				printJobResults(returns, module, format)
			}
			return nil
		}
	}
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
func moduleTimeout(cmd *cobra.Command, module string) time.Duration {
	// Check if the user explicitly set --timeout.
	if cmd.Flags().Changed("timeout") {
		t, _ := cmd.Flags().GetDuration("timeout")
		return t
	}

	switch module {
	case "state.apply":
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

	case "settings.items":
		cliargs.ParseKeyValues(remaining, args)
		return "items", args, nil

	case "settings.get":
		if len(remaining) == 0 {
			return "", nil, fmt.Errorf("settings.get requires a key argument")
		}
		args["key"] = remaining[0]
		cliargs.ParseKeyValues(remaining[1:], args)
		return remaining[0], args, nil

	case "settings.keys":
		cliargs.ParseKeyValues(remaining, args)
		return "keys", args, nil

	case "state.apply":
		if len(remaining) == 0 {
			return "", nil, fmt.Errorf("state.apply requires a state name argument")
		}
		state := remaining[0]
		args["state"] = state
		cliargs.ParseKeyValues(remaining[1:], args)
		return state, args, nil

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
		return "", nil, true, fmt.Errorf("%s requires a %s argument", module, primary.Name)
	}
	args = make(map[string]any)
	args[primary.Name] = remaining[0]
	cliargs.ParseKeyValues(remaining[1:], args)
	return remaining[0], args, true, nil
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
