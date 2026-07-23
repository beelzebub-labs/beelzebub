// Command validate-specs validates all honeypot service configuration files
// against the per-protocol JSON Schemas in specs/.
//
// Usage:
//
//	go run ./cmd/validate-specs
//	go run ./cmd/validate-specs -configs path/to/configs
//	go run ./cmd/validate-specs -specs path/to/specs
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"gopkg.in/yaml.v3"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	// Keep the default help header compatible with flag.CommandLine.
	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	configsDir := fs.String("configs", "configurations/services", "directory with YAML config files")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	absConfigs, err := filepath.Abs(*configsDir)
	if err != nil {
		fmt.Fprintf(stderr, "error: resolving configs path: %v\n", err)
		return 1
	}

	entries, err := os.ReadDir(absConfigs)
	if err != nil {
		fmt.Fprintf(stderr, "error: reading configs dir: %v\n", err)
		return 1
	}

	type result struct {
		File   string
		Errors []string
	}

	var results []result
	total := 0

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		total++

		filePath := filepath.Join(absConfigs, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			results = append(results, result{File: entry.Name(), Errors: []string{fmt.Sprintf("reading file: %v", err)}})
			continue
		}

		var svc parser.BeelzebubServiceConfiguration
		if err := yaml.Unmarshal(data, &svc); err != nil {
			results = append(results, result{File: entry.Name(), Errors: []string{fmt.Sprintf("parsing YAML: %v", err)}})
			continue
		}
		svc.Filename = entry.Name()

		issues := parser.ValidateConfigSchema(svc)
		if len(issues) == 0 {
			results = append(results, result{File: entry.Name()})
		} else {
			errs := make([]string, len(issues))
			for i, iss := range issues {
				errs[i] = iss.Message
			}
			results = append(results, result{File: entry.Name(), Errors: errs})
		}
	}

	passed := 0
	failed := 0

	for _, r := range results {
		if len(r.Errors) == 0 {
			fmt.Fprintf(stdout, "✓ %s\n", r.File)
			passed++
		} else {
			fmt.Fprintf(stdout, "✗ %s\n", r.File)
			for _, e := range r.Errors {
				fmt.Fprintf(stdout, "    %s\n", e)
			}
			failed++
		}
	}

	fmt.Fprintf(stdout, "\n%d files: %d passed, %d failed\n", total, passed, failed)

	if failed > 0 {
		return 1
	}

	return 0
}
