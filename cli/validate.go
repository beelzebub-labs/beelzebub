package cli

import (
	"fmt"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/spf13/cobra"

	_ "github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/HTTP"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/MCP"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/SSH"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/TCP"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/TELNET"
)

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate configuration files without starting services",
	Long:  "Parse and validate core and service YAML configurations, reporting any errors and warnings.",
	RunE:  validateConfigurations,
}

func init() {
}

func validateConfigurations(_ *cobra.Command, _ []string) error {
	return runValidate(rootConfCore, rootConfServices)
}

func runValidate(corePath, servicesPath string) error {
	fmt.Printf("Validating services from %s\n", servicesPath)
	fmt.Printf("Validating core from %s\n\n", corePath)

	p := parser.Init(corePath, servicesPath)

	services, parseIssues, err := p.ReadConfigurationsServicesForValidation()
	if err != nil {
		return fmt.Errorf("services config: %w", err)
	}

	serviceResult := parser.Validate(services, parseIssues)

	var coreResult parser.ValidateResult
	coreConf, err := p.ReadConfigurationsCore()
	if err != nil {
		coreResult = parser.ValidateResult{
			Results: []parser.ValidationResult{
				{Filename: corePath, Issues: []parser.ValidationIssue{
					{Level: parser.LevelError, Message: fmt.Sprintf("failed to read core config: %v", err)},
				}},
			},
			TotalErrors: 1,
		}
	} else {
		coreResult = parser.ValidateCore(coreConf, corePath)
	}

	combined := parser.ValidateResult{
		Results:       append(serviceResult.Results, coreResult.Results...),
		TotalErrors:   serviceResult.TotalErrors + coreResult.TotalErrors,
		TotalWarnings: serviceResult.TotalWarnings + coreResult.TotalWarnings,
	}

	combined.Print()

	if combined.ExitCode() != 0 {
		return fmt.Errorf("validation failed with %d error(s)", combined.TotalErrors)
	}

	return nil
}

func printSection(title, detail string) {
	fmt.Printf("%s: %s\n", title, detail)
}

func printField(name, value string) {
	fmt.Printf("  %-18s %s\n", name+":", value)
}

func formatBool(v bool) string {
	if v {
		return "enabled"
	}
	return "disabled"
}

func formatOptional(s string) string {
	if s == "" {
		return "(not set)"
	}
	return s
}
