package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"slices"
	"strconv"
	"strings"
	"sync"
)

const (
	LevelError   = "error"
	LevelWarning = "warning"
	minPort      = 1
	maxPort      = 65535
)

// ValidationIssue is a single validation finding, either an error or a warning
type ValidationIssue struct {
	Level    string
	Message  string
	Filename string
}

// ValidationResult holds all validation issues for a single configuration file
type ValidationResult struct {
	Filename string
	Issues   []ValidationIssue
}

// ValidateResult aggregates validation results across all configuration files
type ValidateResult struct {
	Results       []ValidationResult
	TotalErrors   int
	TotalWarnings int
}

// ServiceValidator is the interface that protocol and plugin validators implement
type ServiceValidator interface {
	Name() string
	Validate(config BeelzebubServiceConfiguration) []ValidationIssue
}

var (
	serviceValidators []ServiceValidator
	serviceValidatorsMu sync.Mutex
)

// RegisterServiceValidator adds a ServiceValidator to the global registry
func RegisterServiceValidator(v ServiceValidator) {
	serviceValidatorsMu.Lock()
	defer serviceValidatorsMu.Unlock()
	serviceValidators = append(serviceValidators, v)
}

// ResetServiceValidators clears the global validator registry (for test isolation)
func ResetServiceValidators() {
	serviceValidatorsMu.Lock()
	defer serviceValidatorsMu.Unlock()
	serviceValidators = nil
}

// GetServiceValidators returns a copy of the current validator registry
func GetServiceValidators() []ServiceValidator {
	serviceValidatorsMu.Lock()
	defer serviceValidatorsMu.Unlock()
	return append([]ServiceValidator(nil), serviceValidators...)
}

var validCommandPlugins = []string{"", "LLMHoneypot", "MazeHoneypot"}

var validCommandPluginsDisplay = []string{"(none)", "LLMHoneypot", "MazeHoneypot"}

// Validate checks service configurations and returns all errors and warnings
func Validate(services []BeelzebubServiceConfiguration, parseIssues []ValidationIssue) ValidateResult {
	resultMap := make(map[string]*ValidationResult)

	for _, issue := range parseIssues {
		r, ok := resultMap[issue.Filename]
		if !ok {
			r = &ValidationResult{Filename: issue.Filename}
			resultMap[issue.Filename] = r
		}
		r.Issues = append(r.Issues, issue)
	}

	for i := range services {
		r := getResult(resultMap, services[i].Filename)
		services[i].Address = strings.TrimSpace(services[i].Address)

		r.Issues = append(r.Issues, validateAddress(services[i])...)
		r.Issues = append(r.Issues, validateCommands(services[i])...)
		r.Issues = append(r.Issues, validateFallbackCommand(services[i])...)
		r.Issues = append(r.Issues, validatePluginConfig(services[i])...)

		for _, v := range GetServiceValidators() {
			r.Issues = append(r.Issues, v.Validate(services[i])...)
		}
	}

	detectCollisions(services, resultMap)

	return buildResult(resultMap)
}

func getResult(resultMap map[string]*ValidationResult, filename string) *ValidationResult {
	r, ok := resultMap[filename]
	if !ok {
		r = &ValidationResult{Filename: filename}
		resultMap[filename] = r
	}
	return r
}

func validateAddress(svc BeelzebubServiceConfiguration) []ValidationIssue {
	address := svc.Address
	if address == "" {
		return []ValidationIssue{{Level: LevelError, Message: "address is empty"}}
	}

	if strings.Contains(address, "/") {
		return nil
	}

	lastColon := strings.LastIndex(address, ":")
	if lastColon == -1 {
		return []ValidationIssue{{
			Level:   LevelWarning,
			Message: fmt.Sprintf("address %q has invalid port format or port out of range (%d-%d)", address, minPort, maxPort),
		}}
	}
	portStr := address[lastColon+1:]
	port, err := strconv.Atoi(portStr)
	if err != nil || port < minPort || port > maxPort {
		return []ValidationIssue{{
			Level:   LevelWarning,
			Message: fmt.Sprintf("address %q has invalid port format or port out of range (%d-%d)", address, minPort, maxPort),
		}}
	}
	return nil
}

func validateCommands(svc BeelzebubServiceConfiguration) []ValidationIssue {
	var issues []ValidationIssue
	for j, cmd := range svc.Commands {
		if cmd.RegexStr == "" {
			issues = append(issues, ValidationIssue{
				Level:   LevelError,
				Message: fmt.Sprintf("command[%d] has empty regex", j),
			})
		}

		if !slices.Contains(validCommandPlugins, cmd.Plugin) {
			issues = append(issues, ValidationIssue{
				Level:   LevelError,
				Message: fmt.Sprintf("command[%d] has invalid plugin %q, valid: %s", j, cmd.Plugin, strings.Join(validCommandPluginsDisplay, ", ")),
			})
		}

		if cmd.Handler == "" && cmd.Plugin == "" {
			issues = append(issues, ValidationIssue{
				Level:   LevelWarning,
				Message: fmt.Sprintf("command[%d] has empty handler and no plugin, matched requests will produce no output", j),
			})
		}

		for _, header := range cmd.Headers {
			if !strings.Contains(header, ":") {
				issues = append(issues, ValidationIssue{
					Level:   LevelWarning,
					Message: fmt.Sprintf("command[%d].headers has malformed entry (no colon): %q", j, header),
				})
			}
		}
	}
	return issues
}

func validateFallbackCommand(svc BeelzebubServiceConfiguration) []ValidationIssue {
	var issues []ValidationIssue
	fb := svc.FallbackCommand
	if fb.Handler == "" && fb.Plugin == "" {
		return nil
	}

	if fb.RegexStr != "" {
		if _, err := regexp.Compile(fb.RegexStr); err != nil {
			issues = append(issues, ValidationIssue{
				Level:   LevelError,
				Message: fmt.Sprintf("fallbackCommand has invalid regex: %v", err),
			})
		}
	}

	if !slices.Contains(validCommandPlugins, fb.Plugin) {
		issues = append(issues, ValidationIssue{
			Level:   LevelError,
			Message: fmt.Sprintf("fallbackCommand has invalid plugin %q, valid: %s", fb.Plugin, strings.Join(validCommandPluginsDisplay, ", ")),
		})
	}
	return issues
}

func validatePluginConfig(svc BeelzebubServiceConfiguration) []ValidationIssue {
	var issues []ValidationIssue
	if svc.DeadlineTimeoutSeconds == 0 && len(svc.Commands) > 0 {
		issues = append(issues, ValidationIssue{
			Level:   LevelWarning,
			Message: "deadlineTimeoutSeconds is not set, connections may be closed immediately",
		})
	}

	if svc.Plugin.OpenAISecretKey != "" {
		issues = append(issues, ValidationIssue{
			Level:   LevelWarning,
			Message: "openAISecretKey is set inline in config — prefer using the OPEN_AI_SECRET_KEY environment variable to avoid exposing secrets in version control",
		})
	}
	return issues
}

func detectCollisions(services []BeelzebubServiceConfiguration, resultMap map[string]*ValidationResult) {
	collisionMap := make(map[string][]int)
	for i, svc := range services {
		key := svc.Protocol + " " + strings.TrimSpace(svc.Address)
		collisionMap[key] = append(collisionMap[key], i)
	}

	for _, indices := range collisionMap {
		if len(indices) > 1 {
			for _, idx := range indices {
				svc := services[idx]
				r := resultMap[svc.Filename]
				r.Issues = append(r.Issues, ValidationIssue{
					Level:   LevelError,
					Message: fmt.Sprintf("address %s:%s is used by multiple services", svc.Protocol, svc.Address),
				})
			}
		}
	}
}

func buildResult(resultMap map[string]*ValidationResult) ValidateResult {
	var results []ValidationResult
	var totalErrors, totalWarnings int
	for _, r := range resultMap {
		results = append(results, *r)

		for _, issue := range r.Issues {
			switch issue.Level {
			case LevelError:
				totalErrors++
			case LevelWarning:
				totalWarnings++
			}
		}
	}
	return ValidateResult{
		Results:       results,
		TotalErrors:   totalErrors,
		TotalWarnings: totalWarnings,
	}
}

// Print writes validation results to stdout, sorted by filename
func (r ValidateResult) Print() {
	sort.Slice(r.Results, func(i, j int) bool {
		return r.Results[i].Filename < r.Results[j].Filename
	})

	for _, result := range r.Results {
		hasErrors := false
		for _, issue := range result.Issues {
			if issue.Level == LevelError {
				hasErrors = true
				break
			}
		}

		if len(result.Issues) == 0 {
			fmt.Printf("OK %s\n", result.Filename)
		} else if hasErrors {
			fmt.Printf("FAIL %s\n", result.Filename)
			for _, issue := range result.Issues {
				fmt.Printf("  %s: %s\n", strings.ToUpper(issue.Level), issue.Message)
			}
		} else {
			fmt.Printf("WARN %s\n", result.Filename)
			for _, issue := range result.Issues {
				fmt.Printf("  %s: %s\n", strings.ToUpper(issue.Level), issue.Message)
			}
		}
	}

	fmt.Printf("\n%d errors, %d warnings\n", r.TotalErrors, r.TotalWarnings)
}

// ExitCode returns 1 if there are errors, 0 otherwise
func (r ValidateResult) ExitCode() int {
	if r.TotalErrors > 0 {
		return 1
	}
	return 0
}

// ValidateCore checks the core configuration and returns all errors and warnings
func ValidateCore(config *BeelzebubCoreConfigurations, filename string) ValidateResult {
	var issues []ValidationIssue

	if config.Core.Tracings.RabbitMQ.Enabled && config.Core.Tracings.RabbitMQ.URI == "" {
		issues = append(issues, ValidationIssue{
			Level:    LevelError,
			Message:  "rabbitMQ is enabled but URI is empty",
			Filename: filename,
		})
	}

	if config.Core.BeelzebubCloud.Enabled {
		if config.Core.BeelzebubCloud.URI == "" {
			issues = append(issues, ValidationIssue{
				Level:    LevelError,
				Message:  "beelzebub-cloud is enabled but URI is empty",
				Filename: filename,
			})
		}
		if config.Core.BeelzebubCloud.AuthToken == "" {
			issues = append(issues, ValidationIssue{
				Level:    LevelError,
				Message:  "beelzebub-cloud is enabled but auth-token is empty",
				Filename: filename,
			})
		}
	}

	if config.Core.Prometheus != (Prometheus{}) {
		if config.Core.Prometheus.Path == "" {
			issues = append(issues, ValidationIssue{
				Level:    LevelError,
				Message:  "prometheus is configured but path is empty",
				Filename: filename,
			})
		} else if !strings.HasPrefix(config.Core.Prometheus.Path, "/") {
			issues = append(issues, ValidationIssue{
				Level:    LevelError,
				Message:  fmt.Sprintf("prometheus path %q must start with /", config.Core.Prometheus.Path),
				Filename: filename,
			})
		}
		if config.Core.Prometheus.Port == "" {
			issues = append(issues, ValidationIssue{
				Level:    LevelError,
				Message:  "prometheus is configured but port is empty",
				Filename: filename,
			})
		}
	}

	if config.Core.Logging.LogsPath != "" {
		parentDir := filepath.Dir(config.Core.Logging.LogsPath)
		if _, err := os.Stat(parentDir); os.IsNotExist(err) {
			issues = append(issues, ValidationIssue{
				Level:    LevelWarning,
				Message:  fmt.Sprintf("logs path %q: parent directory %q does not exist", config.Core.Logging.LogsPath, parentDir),
				Filename: filename,
			})
		}
	}

	var totalErrors int
	for _, issue := range issues {
		if issue.Level == LevelError {
			totalErrors++
		}
	}

	return ValidateResult{
		Results: []ValidationResult{
			{Filename: filename, Issues: issues},
		},
		TotalErrors:   totalErrors,
		TotalWarnings: len(issues) - totalErrors,
	}
}
