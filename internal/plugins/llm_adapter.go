package plugins

import (
	"context"
	"fmt"

	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
)

// llmPlugin is the registry adapter for LLMHoneypot.
// It bridges the pkg/plugin.CommandPlugin interface to the internal LLMHoneypot implementation.
type llmPlugin struct{}

func (l *llmPlugin) Metadata() plugin.Metadata {
	return plugin.Metadata{
		Name:        LLMPluginName,
		Description: "LLM-powered response generator — emulates realistic system behaviour via OpenAI, Ollama, or OrcaRouter",
		Version:     "1.0.0",
		Author:      "beelzebub",
	}
}

func (l *llmPlugin) Execute(ctx context.Context, req plugin.CommandRequest) (string, error) {
	llmProvider, err := FromStringToLLMProvider(req.Config.LLMProvider)
	if err != nil {
		return "", fmt.Errorf("llm plugin: %w", err)
	}

	proto, ok := tracer.ProtocolFromString(req.Protocol)
	if !ok {
		return "", fmt.Errorf("llm plugin: unknown protocol %q", req.Protocol)
	}

	hp := &LLMHoneypot{
		Histories:               MessagesFromPlugin(req.History),
		OpenAIKey:               req.Config.OpenAISecretKey,
		OrcaRouterKey:           req.Config.OrcaRouterSecretKey,
		Protocol:                proto,
		Host:                    req.Config.Host,
		Model:                   req.Config.LLMModel,
		Provider:                llmProvider,
		CustomPrompt:            req.Config.Prompt,
		InputValidationEnabled:  req.Config.InputValidationEnabled,
		InputValidationPrompt:   req.Config.InputValidationPrompt,
		OutputValidationEnabled: req.Config.OutputValidationEnabled,
		OutputValidationPrompt:  req.Config.OutputValidationPrompt,
		RateLimitEnabled:        req.Config.RateLimitEnabled,
		RateLimitRequests:       req.Config.RateLimitRequests,
		RateLimitWindowSeconds:  req.Config.RateLimitWindowSeconds,
	}

	return InitLLMHoneypot(*hp).ExecuteModel(req.Command, req.ClientIP)
}

func init() {
	plugin.Register(&llmPlugin{})
}
