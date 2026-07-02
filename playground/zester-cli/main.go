// Command zester-cli provides a Salt-like CLI for interacting with Zester peels.
//
// Usage:
//
//	zester-cli <target> <module.function> [id] [key=value ...]
//
// Examples:
//
//	zester-cli 'web-01' cmd.run 'uptime'
//	zester-cli '*' cmd.run 'hostname'
//	zester-cli 'web-01' state.apply webserver
//	zester-cli 'web-01' file.managed /tmp/test.txt content="hello" mode=0644
//	zester-cli 'web-01' pkg.installed curl
//	zester-cli '*' test.ping
//	zester-cli 'web-01' facts.items
//	zester-cli 'web-01' facts.get os.family
//	zester-cli 'web-01' facts.keys
//	zester-cli 'web-01' facts.set datacenter us-east-1
//	zester-cli 'web-01' settings.items
//	zester-cli 'web-01' settings.get database.port
//	zester-cli 'web-01' settings.get database.port default=5432
//	zester-cli 'web-01' settings.keys
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/proto"
	"github.com/segmentio/ksuid"
	"gopkg.in/yaml.v3"
)

// ANSI color codes.
const (
	colorGreen = "\033[32m"
	colorRed   = "\033[91m"
	colorReset = "\033[0m"
)

// outputRecord is the structured representation of a single peel's result,
// used for JSON and YAML output.
type outputRecord struct {
	PeelID  string              `json:"peel_id" yaml:"peel_id"`
	Success bool                `json:"success" yaml:"success"`
	Results []outputStateResult `json:"results" yaml:"results"`
	Error   string              `json:"error" yaml:"error"`
}

type outputStateResult struct {
	Name       string            `json:"name" yaml:"name"`
	Changed    bool              `json:"changed" yaml:"changed"`
	Details    map[string]string `json:"details,omitempty" yaml:"details,omitempty"`
	Duration   time.Duration     `json:"duration" yaml:"duration"`
	Diff       string            `json:"diff,omitempty" yaml:"diff,omitempty"`
	Error      string            `json:"error,omitempty" yaml:"error,omitempty"`
	Skipped    bool              `json:"skipped,omitempty" yaml:"skipped,omitempty"`
	SkipReason string            `json:"skip_reason,omitempty" yaml:"skip_reason,omitempty"`
}

// result holds a single peel's response or error from execution.
type result struct {
	peelID string
	resp   *proto.ExecResponse
	err    error
}

func main() {
	natsURL := flag.String("url", "", "NATS server URL (default: $NATS_URL or nats://nats:4222)")
	timeout := flag.Duration("timeout", 30*time.Second, "request timeout")
	credsFile := flag.String("creds", "", "NATS credentials file (default: $ZESTER_CREDS or /data/auth/admin.creds)")
	format := flag.String("format", "text", "output format: text, json, yaml")
	noColor := flag.Bool("no-color", false, "disable colored output")
	flag.Parse()

	args := flag.Args()
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: zester-cli [--url URL] [--timeout DURATION] [--format text|json|yaml] [--no-color] <target> <module.function> [args...]\n")
		os.Exit(1)
	}

	target := args[0]
	module := args[1]
	remaining := args[2:]

	// Build ExecRequest from module-specific argument parsing.
	id, modArgs, err := parseModuleArgs(module, remaining)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Resolve NATS URL: flag > env > default.
	url := *natsURL
	if url == "" {
		url = os.Getenv("NATS_URL")
	}
	if url == "" {
		url = "nats://nats:4222"
	}

	// Resolve creds: flag > env > default.
	creds := *credsFile
	if creds == "" {
		creds = os.Getenv("ZESTER_CREDS")
	}
	if creds == "" {
		creds = "/data/auth/admin.creds"
	}

	var opts []nats.Option
	if creds != "" {
		opts = append(opts, nats.UserCredentials(creds))
	}

	nc, err := nats.Connect(url, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: connect to NATS %s: %v\n", url, err)
		os.Exit(1)
	}
	defer nc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// Resolve target to a list of peel IDs.
	peelIDs, err := resolveTargets(ctx, nc, target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: resolve targets: %v\n", err)
		os.Exit(1)
	}
	if len(peelIDs) == 0 {
		fmt.Fprintf(os.Stderr, "error: no peels matched target %q\n", target)
		os.Exit(1)
	}

	// Determine whether color is enabled.
	useColor := !(*noColor) && shouldColor()

	// Send requests concurrently.
	results := make([]result, len(peelIDs))
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
				results[idx] = result{peelID: pid, err: fmt.Errorf("encode request: %w", err)}
				return
			}

			msg, err := nc.RequestWithContext(ctx, bus.CmdSubject(pid), data)
			if err != nil {
				results[idx] = result{peelID: pid, err: err}
				return
			}

			var resp proto.ExecResponse
			if err := bus.Decode(msg.Data, &resp); err != nil {
				results[idx] = result{peelID: pid, err: fmt.Errorf("decode response: %w", err)}
				return
			}

			results[idx] = result{peelID: pid, resp: &resp}
		}(i, peelID)
	}

	wg.Wait()

	// Output based on format.
	hasError := false
	for _, r := range results {
		if r.err != nil || (r.resp != nil && !r.resp.Success) {
			hasError = true
			break
		}
	}

	switch *format {
	case "json":
		printJSON(results, module)
	case "yaml":
		printYAML(results, module)
	default:
		for _, r := range results {
			printResultText(r.peelID, module, r.resp, r.err, useColor)
		}
	}

	if hasError {
		os.Exit(1)
	}
}

// shouldColor returns true if stdout is a TTY and NO_COLOR env is not set.
func shouldColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// colorize wraps text in ANSI color codes if color is enabled.
func colorize(text, color string, enabled bool) string {
	if !enabled {
		return text
	}
	return color + text + colorReset
}

// parseModuleArgs parses remaining CLI arguments into an ID and args map
// based on the module type.
func parseModuleArgs(module string, remaining []string) (string, map[string]any, error) {
	args := make(map[string]any)

	switch module {
	case "cmd.run":
		if len(remaining) == 0 {
			return "", nil, fmt.Errorf("cmd.run requires a command argument")
		}
		args["command"] = remaining[0]
		parseKeyValues(remaining[1:], args)
		return "ad-hoc", args, nil

	case "test.ping":
		parseKeyValues(remaining, args)
		return "ping", args, nil

	case "facts.items":
		parseKeyValues(remaining, args)
		return "items", args, nil

	case "facts.get":
		if len(remaining) == 0 {
			return "", nil, fmt.Errorf("facts.get requires a key argument")
		}
		args["key"] = remaining[0]
		parseKeyValues(remaining[1:], args)
		return remaining[0], args, nil

	case "facts.keys":
		parseKeyValues(remaining, args)
		return "keys", args, nil

	case "facts.set":
		if len(remaining) < 2 {
			return "", nil, fmt.Errorf("facts.set requires a key and value argument")
		}
		args["key"] = remaining[0]
		args["value"] = remaining[1]
		parseKeyValues(remaining[2:], args)
		return remaining[0], args, nil

	case "settings.items":
		parseKeyValues(remaining, args)
		return "items", args, nil

	case "settings.get":
		if len(remaining) == 0 {
			return "", nil, fmt.Errorf("settings.get requires a key argument")
		}
		args["key"] = remaining[0]
		parseKeyValues(remaining[1:], args)
		return remaining[0], args, nil

	case "settings.keys":
		parseKeyValues(remaining, args)
		return "keys", args, nil

	case "file.managed":
		if len(remaining) == 0 {
			return "", nil, fmt.Errorf("file.managed requires a path argument")
		}
		path := remaining[0]
		args["path"] = path
		parseKeyValues(remaining[1:], args)
		return path, args, nil

	case "pkg.installed":
		if len(remaining) == 0 {
			return "", nil, fmt.Errorf("pkg.installed requires a package name argument")
		}
		name := remaining[0]
		args["name"] = name
		parseKeyValues(remaining[1:], args)
		return name, args, nil

	case "state.apply":
		if len(remaining) == 0 {
			return "", nil, fmt.Errorf("state.apply requires a state name argument")
		}
		state := remaining[0]
		args["state"] = state
		parseKeyValues(remaining[1:], args)
		return state, args, nil

	case "state.highstate":
		parseKeyValues(remaining, args)
		return "highstate", args, nil

	default:
		// Generic fallback: first arg is the ID, rest are key=value.
		id := "ad-hoc"
		if len(remaining) > 0 {
			id = remaining[0]
			parseKeyValues(remaining[1:], args)
		}
		return id, args, nil
	}
}

// parseKeyValues parses key=value pairs from a slice and adds them to the args map.
func parseKeyValues(pairs []string, args map[string]any) {
	for _, pair := range pairs {
		if k, v, ok := strings.Cut(pair, "="); ok {
			args[k] = v
		}
	}
}

// resolveTargets resolves a target pattern to a list of peel IDs.
// If the target contains glob characters, it lists all keys from the facts
// KV bucket and filters with filepath.Match.
func resolveTargets(ctx context.Context, nc *nats.Conn, target string) ([]string, error) {
	if !isGlob(target) {
		return []string{target}, nil
	}

	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("create jetstream context: %w", err)
	}

	kv, err := bus.GetBucket(ctx, bus.NewJS(js), bus.BucketFacts)
	if err != nil {
		return nil, fmt.Errorf("get facts bucket: %w", err)
	}

	keys, err := kv.Keys(ctx)
	if err != nil {
		return nil, fmt.Errorf("list fact keys: %w", err)
	}

	var matched []string
	for _, key := range keys {
		ok, err := filepath.Match(target, key)
		if err != nil {
			return nil, fmt.Errorf("invalid glob pattern %q: %w", target, err)
		}
		if ok {
			matched = append(matched, key)
		}
	}

	return matched, nil
}

// isGlob returns true if the pattern contains glob metacharacters.
func isGlob(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

// isStreamlined returns true for modules whose output should show only
// stdout/stderr rather than full detail fields.
func isStreamlined(module string) bool {
	return module == "cmd.run" || module == "test.ping" || strings.HasPrefix(module, "facts.") || strings.HasPrefix(module, "settings.")
}

// printResultText prints a single peel's result in the text format.
func printResultText(peelID, module string, resp *proto.ExecResponse, err error, useColor bool) {
	// Determine success for coloring the peel name.
	success := err == nil && resp != nil && resp.Success
	color := colorRed
	if success {
		color = colorGreen
	}

	fmt.Printf("%s:\n", colorize(peelID, color, useColor))

	if err != nil {
		fmt.Printf("    ERROR: %v\n", err)
		return
	}

	if resp.Error != "" {
		fmt.Printf("    ERROR: %s\n", resp.Error)
		return
	}

	if isStreamlined(module) {
		for _, sr := range resp.Results {
			// For cmd.run: show stdout, then stderr if non-empty.
			// For test.ping: show the "result" field.
			stdout := sr.Details["stdout"]
			if stdout == "" {
				stdout = sr.Details["result"]
			}
			if stdout != "" {
				for _, line := range strings.Split(stdout, "\n") {
					fmt.Printf("    %s\n", line)
				}
			}
			stderr := sr.Details["stderr"]
			if stderr != "" {
				for _, line := range strings.Split(stderr, "\n") {
					fmt.Printf("    stderr: %s\n", line)
				}
			}
			if sr.Error != "" {
				fmt.Printf("    ERROR: %s\n", sr.Error)
			}
		}
	} else {
		// Detailed format for file.managed, pkg.installed, state.apply, etc.
		for _, sr := range resp.Results {
			fmt.Printf("    %s:\n", module)
			if sr.Name != "" {
				fmt.Printf("        name: %s\n", sr.Name)
			}
			for k, v := range sr.Details {
				fmt.Printf("        %s: %s\n", k, v)
			}
			fmt.Printf("        changed: %t\n", sr.Changed)
			fmt.Printf("        duration: %s\n", sr.Duration)
			if sr.Error != "" {
				fmt.Printf("        error: %s\n", sr.Error)
			}
			if sr.Diff != "" {
				fmt.Printf("        diff: %s\n", sr.Diff)
			}
			if sr.Skipped {
				fmt.Printf("        skipped: true\n")
				if sr.SkipReason != "" {
					fmt.Printf("        skip_reason: %s\n", sr.SkipReason)
				}
			}
		}
	}
}

// toOutputRecords converts internal results to the structured output format.
func toOutputRecords(results []result, module string) []outputRecord {
	records := make([]outputRecord, 0, len(results))
	for _, r := range results {
		rec := outputRecord{PeelID: r.peelID}
		if r.err != nil {
			rec.Error = r.err.Error()
		} else if r.resp != nil {
			rec.Success = r.resp.Success
			rec.Error = r.resp.Error
			for _, sr := range r.resp.Results {
				rec.Results = append(rec.Results, outputStateResult{
					Name:       sr.Name,
					Changed:    sr.Changed,
					Details:    sr.Details,
					Duration:   sr.Duration,
					Diff:       sr.Diff,
					Error:      sr.Error,
					Skipped:    sr.Skipped,
					SkipReason: sr.SkipReason,
				})
			}
		}
		records = append(records, rec)
	}
	return records
}

// printJSON prints all results as a JSON array.
func printJSON(results []result, module string) {
	records := toOutputRecords(results, module)
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: marshal JSON: %v\n", err)
		return
	}
	fmt.Println(string(data))
}

// printYAML prints all results as YAML documents separated by ---.
func printYAML(results []result, module string) {
	records := toOutputRecords(results, module)
	for i, rec := range records {
		if i > 0 {
			fmt.Println("---")
		}
		data, err := yaml.Marshal(rec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: marshal YAML: %v\n", err)
			return
		}
		fmt.Print(string(data))
	}
}
