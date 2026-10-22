package plugins

import (
	"fmt"
	"os"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
)

type LLMPluginValidator struct{}

func (v *LLMPluginValidator) Name() string {
	return LLMPluginName
}

func (v *LLMPluginValidator) Validate(config parser.BeelzebubServiceConfiguration) []parser.ValidationIssue {
	if !usesPlugin(config, LLMPluginName) {
		return nil
	}

	var issues []parser.ValidationIssue

	switch config.Plugin.LLMProvider {
	case "ollama", "openai", "orcarouter":
	case "":
		issues = append(issues, parser.ValidationIssue{
			Level:   parser.LevelError,
			Message: fmt.Sprintf("plugin LLMHoneypot requires llmProvider (valid: %s)", llmProviderNames),
		})
	default:
		issues = append(issues, parser.ValidationIssue{
			Level:   parser.LevelError,
			Message: fmt.Sprintf("invalid llmProvider %q, valid: %s", config.Plugin.LLMProvider, llmProviderNames),
		})
	}

	if config.Plugin.LLMModel == "" {
		issues = append(issues, parser.ValidationIssue{
			Level:   parser.LevelError,
			Message: "plugin LLMHoneypot requires llmModel",
		})
	}

	if config.Plugin.LLMProvider == "openai" && config.Plugin.OpenAISecretKey == "" && os.Getenv("OPEN_AI_SECRET_KEY") == "" {
		issues = append(issues, parser.ValidationIssue{
			Level:   parser.LevelWarning,
			Message: "openAISecretKey is empty for openai provider, set OPEN_AI_SECRET_KEY env var at runtime or openAISecretKey in config",
		})
	}

	if config.Plugin.LLMProvider == "orcarouter" && config.Plugin.OrcaRouterSecretKey == "" && os.Getenv(orcaRouterKeyEnv) == "" {
		issues = append(issues, parser.ValidationIssue{
			Level:   parser.LevelWarning,
			Message: fmt.Sprintf("orcarouterSecretKey is empty for orcarouter provider, set %s env var at runtime or orcarouterSecretKey in config", orcaRouterKeyEnv),
		})
	}

	return issues
}

func usesPlugin(config parser.BeelzebubServiceConfiguration, pluginName string) bool {
	for _, cmd := range config.Commands {
		if cmd.Plugin == pluginName {
			return true
		}
	}
	if config.FallbackCommand.Plugin == pluginName {
		return true
	}
	return false
}

func init() {
	parser.RegisterServiceValidator(&LLMPluginValidator{})
}
