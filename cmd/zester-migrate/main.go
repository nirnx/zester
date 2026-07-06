package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nirnx/zester/pkg/migrate"
)

func main() {
	write := flag.Bool("write", false, "Write converted .zy files (otherwise just prints report)")
	renameVars := flag.Bool("rename-vars", false, "Also rename grains→facts and pillar→settings")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: zester-migrate [flags] <path>\n\n")
		fmt.Fprintf(os.Stderr, "Convert Salt .sls files to Zester .zy format.\n\n")
		fmt.Fprintf(os.Stderr, "Arguments:\n")
		fmt.Fprintf(os.Stderr, "  <path>    File or directory to migrate\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	path := flag.Arg(0)
	opts := migrate.Options{
		RenameVars: *renameVars,
	}

	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(2)
	}

	var results []*migrate.Result
	if info.IsDir() {
		results, err = migrate.MigrateDir(path, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(2)
		}
	} else {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(2)
		}
		results = []*migrate.Result{migrate.Migrate(path, string(data), opts)}
	}

	hasWarnings := false
	for _, r := range results {
		report := migrate.FormatReport(r)
		if report != "" {
			fmt.Print(report)
		}

		if *write {
			if len(r.Changes) == 0 && len(r.Warnings) == 0 {
				fmt.Println(migrate.FormatWriteAction(r, false))
				continue
			}

			outPath := r.NewPath
			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				fmt.Fprintf(os.Stderr, "Error creating directory for %s: %v\n", outPath, err)
				os.Exit(2)
			}
			if err := os.WriteFile(outPath, []byte(r.Content), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", outPath, err)
				os.Exit(2)
			}
			fmt.Println(migrate.FormatWriteAction(r, true))
		}

		if len(r.Warnings) > 0 {
			hasWarnings = true
		}
	}

	if len(results) > 1 || info.IsDir() {
		fmt.Println()
		fmt.Print(migrate.FormatSummary(results))
	}

	if hasWarnings {
		os.Exit(1)
	}
}
