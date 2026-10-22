package plugins

import (
	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
)

// MessagesToPlugin converts internal plugin.Message slice to pkg/plugin format.
func MessagesToPlugin(messages []Message) []plugin.Message {
	result := make([]plugin.Message, len(messages))
	for i, m := range messages {
		result[i] = plugin.Message{Role: m.Role, Content: m.Content}
	}
	return result
}

// MessagesFromPlugin converts pkg/plugin messages to the internal format.
func MessagesFromPlugin(messages []plugin.Message) []Message {
	result := make([]Message, len(messages))
	for i, m := range messages {
		result[i] = Message{Role: m.Role, Content: m.Content}
	}
	return result
}

// ConfigFromServiceConf builds a plugin.Config from a service configuration.
func ConfigFromServiceConf(servConf parser.BeelzebubServiceConfiguration) plugin.Config {
	return plugin.Config{
		LLMProvider:             servConf.Plugin.LLMProvider,
		LLMModel:                servConf.Plugin.LLMModel,
		OpenAISecretKey:         servConf.Plugin.OpenAISecretKey,
		OrcaRouterSecretKey:     servConf.Plugin.OrcaRouterSecretKey,
		Host:                    servConf.Plugin.Host,
		Prompt:                  servConf.Plugin.Prompt,
		InputValidationEnabled:  servConf.Plugin.InputValidationEnabled,
		InputValidationPrompt:   servConf.Plugin.InputValidationPrompt,
		OutputValidationEnabled: servConf.Plugin.OutputValidationEnabled,
		OutputValidationPrompt:  servConf.Plugin.OutputValidationPrompt,
		RateLimitEnabled:        servConf.Plugin.RateLimitEnabled,
		RateLimitRequests:       servConf.Plugin.RateLimitRequests,
		RateLimitWindowSeconds:  servConf.Plugin.RateLimitWindowSeconds,
		ServerVersion:           servConf.ServerVersion,
		ServerName:              servConf.ServerName,
	}
}
