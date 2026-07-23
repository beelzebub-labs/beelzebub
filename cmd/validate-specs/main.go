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
	"os"
	"path/filepath"
	"strings"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"gopkg.in/yaml.v3"
)

func main() {
	configsDir := flag.String("configs", "configurations/services", "directory with YAML config files")
	flag.Parse()

	absConfigs, err := filepath.Abs(*configsDir)
	if err != nil {
		exit("resolving configs path: %v", err)
	}

	entries, err := os.ReadDir(absConfigs)
	if err != nil {
		exit("reading configs dir: %v", err)
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
			fmt.Printf("✓ %s\n", r.File)
			passed++
		} else {
			fmt.Printf("✗ %s\n", r.File)
			for _, e := range r.Errors {
				fmt.Printf("    %s\n", e)
			}
			failed++
		}
	}

	fmt.Printf("\n%d files: %d passed, %d failed\n", total, passed, failed)

	if failed > 0 {
		os.Exit(1)
	}
}

func exit(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
