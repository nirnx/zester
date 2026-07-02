package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ptorbus/zester/pkg/job"
	"github.com/ptorbus/zester/pkg/proto"
	"gopkg.in/yaml.v3"
)

// ANSI color codes.
const (
	colorGreen = "\033[32m"
	colorRed   = "\033[91m"
	colorReset = "\033[0m"
)

// outputRecord is the structured representation of a single peel's result.
type outputRecord struct {
	PeelID  string              `json:"peel_id" yaml:"peel_id"`
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

// colorize wraps text in ANSI color codes if color is enabled.
func colorize(text, color string, enabled bool) string {
	if !enabled {
		return text
	}
	return color + text + colorReset
}

// isStreamlined returns true for modules whose output should show only
// stdout/stderr rather than full detail fields.
func isStreamlined(module string) bool {
	return module == "cmd.run" || module == "test.ping" ||
		strings.HasPrefix(module, "facts.") || strings.HasPrefix(module, "settings.")
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
	success := ret.Error == "" && ret.Success
	color := colorRed
	if success {
		color = colorGreen
	}

	fmt.Printf("%s:\n", colorize(ret.PeelID, color, useColor))

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
				for _, line := range strings.Split(stdout, "\n") {
					fmt.Printf("    %s\n", line)
				}
			}
			stderr, _ := details["stderr"].(string)
			if stderr != "" {
				for _, line := range strings.Split(stderr, "\n") {
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
			PeelID:  ret.PeelID,
			Success: ret.Success,
			Error:   ret.Error,
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
