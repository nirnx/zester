package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/nirnx/zester/pkg/enroll"
	"github.com/nirnx/zester/pkg/job"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/proto"
)

// ANSI color codes.
const (
	colorGreen      = "\033[32m"
	colorRed        = "\033[91m"
	colorBoldCyan   = "\033[1;36m"
	colorBoldYellow = "\033[1;33m"
	colorReset      = "\033[0m"
)

// displayPeel maps a wire-form peel ID to its human form for CLI output:
// interior '_' decodes back to '.' (enroll.DisplayPeelID), so the operator
// sees "devops-hetzner.oxm", not the NATS subject token "devops-hetzner_oxm".
// The token form is accepted everywhere as INPUT (targets, --peel) but is
// never what the CLI shows.
func displayPeel(id string) string { return enroll.DisplayPeelID(id) }

// displayPeels maps a peel ID list for display.
func displayPeels(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = enroll.DisplayPeelID(id)
	}
	return out
}

// outputRecord is the structured representation of a single peel's result.
type outputRecord struct {
	PeelID string `json:"peel_id" yaml:"peel_id"`
	// Status classifies the outcome for scripts: "success", "failed"
	// (the peel returned an execution failure), or "unreachable" (no
	// heartbeat at dispatch and no ack after republish — the master's
	// synthetic return — or, in direct mode, a transport-level error).
	Status  string              `json:"status" yaml:"status"`
	Success bool                `json:"success" yaml:"success"`
	Results []outputStateResult `json:"results" yaml:"results"`
	Error   string              `json:"error,omitempty" yaml:"error,omitempty"`
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

// colorEnabled is the single color decision for a command's text output:
// --no-color always wins; --force-color enables color even when stdout is not
// a TTY (piping to `less -R`, CI captures) and, being an explicit request,
// overrides the NO_COLOR env; otherwise color follows shouldColor. Commands
// that don't register the flags (test scaffolding) fall through to shouldColor.
func colorEnabled(cmd *cobra.Command) bool {
	if noColor, _ := cmd.Flags().GetBool("no-color"); noColor {
		return false
	}
	if force, _ := cmd.Flags().GetBool("force-color"); force {
		return true
	}
	return shouldColor()
}

// colorize wraps text in ANSI color codes if color is enabled.
func colorize(text, color string, enabled bool) string {
	if !enabled {
		return text
	}
	return color + text + colorReset
}

// isStreamlined returns true for modules whose output should show only
// stdout/stderr rather than full detail fields. sys.doc rides its rendered
// text in Details["result"] (keystone spec §7); keying on the module name here
// is safe before the peel-side sys.doc lands — the CLI just prints whatever
// result text a supporting peel returns.
func isStreamlined(module string) bool {
	return module == "cmd.run" || module == "test.ping" || module == "sys.doc" ||
		strings.HasPrefix(module, "facts.") || strings.HasPrefix(module, "settings.")
}

// streamlinedResultText post-processes a streamlined module's result text for
// display. sys.doc replies are RenderText documents, colorized client-side
// (the wire stays plain text; modschema.ColorizeDoc leaves non-grammar lines —
// the bare-invocation name index included — untouched). Every other module's
// output (cmd.run stdout above all) is NEVER rewritten.
func streamlinedResultText(module, text string, useColor bool) string {
	if module == "sys.doc" && useColor {
		return modschema.ColorizeDoc(text)
	}
	return text
}

// --- Direct mode output ---

// printDirectResults prints results from direct mode execution.
func printDirectResults(results []directResult, module, format string, useColor bool) {
	switch format {
	case "json":
		printDirectJSON(results, module)
	case "yaml":
		printDirectYAML(results, module)
	default:
		for _, r := range results {
			printDirectText(r.peelID, module, r.resp, r.err, useColor)
		}
	}
}

// printDirectText prints a single peel's result in text format (direct mode).
func printDirectText(peelID, module string, resp *proto.ExecResponse, err error, useColor bool) {
	success := err == nil && resp != nil && resp.Success
	color := colorRed
	if success {
		color = colorGreen
	}

	fmt.Printf("%s:\n", colorize(displayPeel(peelID), color, useColor))

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
			stdout := sr.Details["stdout"]
			if stdout == "" {
				stdout = sr.Details["result"]
			}
			if stdout != "" {
				stdout = streamlinedResultText(module, stdout, useColor)
				for line := range strings.SplitSeq(stdout, "\n") {
					fmt.Printf("    %s\n", line)
				}
			}
			stderr := sr.Details["stderr"]
			if stderr != "" {
				for line := range strings.SplitSeq(stderr, "\n") {
					fmt.Printf("    stderr: %s\n", line)
				}
			}
			if sr.Error != "" {
				fmt.Printf("    ERROR: %s\n", sr.Error)
			}
		}
	} else {
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

func directToOutputRecords(results []directResult) []outputRecord {
	records := make([]outputRecord, 0, len(results))
	for _, r := range results {
		rec := outputRecord{PeelID: displayPeel(r.peelID)}
		if r.err != nil {
			// Transport-level failure (no responders for a stopped peel,
			// request timeout): the direct-mode unreachable class.
			rec.Status = "unreachable"
			rec.Error = r.err.Error()
		} else if r.resp != nil {
			rec.Success = r.resp.Success
			rec.Error = r.resp.Error
			rec.Status = "failed"
			if r.resp.Success {
				rec.Status = "success"
			}
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

func printDirectJSON(results []directResult, module string) {
	records := directToOutputRecords(results)
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: marshal JSON: %v\n", err)
		return
	}
	fmt.Println(string(data))
}

func printDirectYAML(results []directResult, module string) {
	records := directToOutputRecords(results)
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

// --- Job mode output ---

// printJobReturnText prints a single job return in text format (streamed as they arrive).
func printJobReturnText(ret job.Return, module string, useColor bool) {
	success := !ret.Unreachable && ret.Error == "" && ret.Success
	color := colorRed
	if success {
		color = colorGreen
	}

	fmt.Printf("%s:\n", colorize(displayPeel(ret.PeelID), color, useColor))

	if ret.Unreachable {
		fmt.Printf("    %s\n", ret.Error)
		return
	}

	if ret.Error != "" {
		fmt.Printf("    ERROR: %s\n", ret.Error)
		return
	}

	// ReturnData contains the ExecResponse decoded as map[string]any (via MessagePack).
	data, ok := ret.ReturnData.(map[string]any)
	if !ok {
		if ret.Success {
			fmt.Println("    OK")
		}
		return
	}

	results, ok := data["results"].([]any)
	if !ok {
		return
	}

	if isStreamlined(module) {
		for _, r := range results {
			rm, ok := r.(map[string]any)
			if !ok {
				continue
			}
			details, _ := rm["details"].(map[string]any)
			stdout, _ := details["stdout"].(string)
			if stdout == "" {
				stdout, _ = details["result"].(string)
			}
			if stdout != "" {
				stdout = streamlinedResultText(module, stdout, useColor)
				for line := range strings.SplitSeq(stdout, "\n") {
					fmt.Printf("    %s\n", line)
				}
			}
			stderr, _ := details["stderr"].(string)
			if stderr != "" {
				for line := range strings.SplitSeq(stderr, "\n") {
					fmt.Printf("    stderr: %s\n", line)
				}
			}
			if errStr, _ := rm["error"].(string); errStr != "" {
				fmt.Printf("    ERROR: %s\n", errStr)
			}
		}
	} else {
		for _, r := range results {
			rm, ok := r.(map[string]any)
			if !ok {
				continue
			}
			name, _ := rm["name"].(string)
			fmt.Printf("    %s:\n", name)
			if errStr, _ := rm["error"].(string); errStr != "" {
				fmt.Printf("        error: %s\n", errStr)
			}
			if skipped, _ := rm["skipped"].(bool); skipped {
				fmt.Println("        skipped: true")
				if reason, _ := rm["skip_reason"].(string); reason != "" {
					fmt.Printf("        skip_reason: %s\n", reason)
				}
			}
			changed, _ := rm["changed"].(bool)
			fmt.Printf("        changed: %t\n", changed)
			if dur, ok := rm["duration"]; ok {
				fmt.Printf("        duration: %v\n", dur)
			}
			if diff, _ := rm["diff"].(string); diff != "" {
				fmt.Printf("        diff: %s\n", diff)
			}
			if details, ok := rm["details"].(map[string]any); ok {
				for k, v := range details {
					fmt.Printf("        %s: %v\n", k, v)
				}
			}
		}
	}
}

// printJobResults prints all job returns in non-text formats (called once after collection).
func printJobResults(returns []job.Return, module, format string) {
	records := jobReturnsToOutputRecords(returns)
	switch format {
	case "json":
		data, err := json.MarshalIndent(records, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: marshal JSON: %v\n", err)
			return
		}
		fmt.Println(string(data))
	case "yaml":
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
}

func jobReturnsToOutputRecords(returns []job.Return) []outputRecord {
	records := make([]outputRecord, 0, len(returns))
	for _, ret := range returns {
		rec := outputRecord{
			PeelID:  displayPeel(ret.PeelID),
			Success: ret.Success,
			Error:   ret.Error,
		}
		switch {
		case ret.Unreachable:
			rec.Status = "unreachable"
		case ret.Success && ret.Error == "":
			rec.Status = "success"
		default:
			rec.Status = "failed"
		}
		// Extract results from ReturnData (MessagePack decoded map).
		if data, ok := ret.ReturnData.(map[string]any); ok {
			if results, ok := data["results"].([]any); ok {
				for _, r := range results {
					rm, ok := r.(map[string]any)
					if !ok {
						continue
					}
					sr := outputStateResult{
						Name: mapStrSafe(rm, "name"),
					}
					sr.Changed, _ = rm["changed"].(bool)
					sr.Diff, _ = rm["diff"].(string)
					sr.Error, _ = rm["error"].(string)
					sr.Skipped, _ = rm["skipped"].(bool)
					sr.SkipReason, _ = rm["skip_reason"].(string)
					if dur, ok := rm["duration"].(int64); ok {
						sr.Duration = time.Duration(dur)
					}
					if details, ok := rm["details"].(map[string]any); ok {
						sr.Details = make(map[string]string, len(details))
						for k, v := range details {
							sr.Details[k] = fmt.Sprintf("%v", v)
						}
					}
					rec.Results = append(rec.Results, sr)
				}
			}
		}
		records = append(records, rec)
	}
	return records
}

func mapStrSafe(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}
