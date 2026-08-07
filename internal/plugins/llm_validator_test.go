package plugins

import (
	"os"
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/stretchr/testify/assert"
)

func makeLLMService(provider, model, secretKey string) parser.BeelzebubServiceConfiguration {
	return parser.BeelzebubServiceConfiguration{
		Commands: []parser.Command{
			{RegexStr: "^(.+)$", Plugin: LLMPluginName},
		},
		Plugin: parser.Plugin{
			LLMProvider:     provider,
			LLMModel:        model,
			OpenAISecretKey: secretKey,
		},
	}
}

func TestLLMPluginValidator_Name(t *testing.T) {
	validator := &LLMPluginValidator{}
	assert.Equal(t, LLMPluginName, validator.Name())
}

func TestLLMPluginValidator_NotUsed(t *testing.T) {
	validator := &LLMPluginValidator{}

	config := parser.BeelzebubServiceConfiguration{
		Commands: []parser.Command{
			{RegexStr: "^(.+)$", Plugin: "SomeOtherPlugin"},
		},
	}

	issues := validator.Validate(config)
	assert.Empty(t, issues)
}

func TestLLMPluginValidator_EmptyProvider(t *testing.T) {
	validator := &LLMPluginValidator{}

	config := makeLLMService("", "llama3", "")

	issues := validator.Validate(config)
	assert.Len(t, issues, 1)
	assert.Equal(t, parser.LevelError, issues[0].Level)
	assert.Contains(t, issues[0].Message, "requires llmProvider")
}

func TestLLMPluginValidator_InvalidProvider(t *testing.T) {
	validator := &LLMPluginValidator{}

	config := makeLLMService("invalid", "llama3", "")

	issues := validator.Validate(config)
	assert.Len(t, issues, 1)
	assert.Equal(t, parser.LevelError, issues[0].Level)
	assert.Contains(t, issues[0].Message, "invalid llmProvider")
}

func TestLLMPluginValidator_ValidOllamaProvider(t *testing.T) {
	validator := &LLMPluginValidator{}

	config := makeLLMService("ollama", "llama3", "")

	issues := validator.Validate(config)
	assert.Empty(t, issues)
}

func TestLLMPluginValidator_ValidOpenAIProvider(t *testing.T) {
	validator := &LLMPluginValidator{}

	config := makeLLMService("openai", "gpt-4o", "sk-test-key")

	issues := validator.Validate(config)
	assert.Empty(t, issues)
}

func TestLLMPluginValidator_EmptyModel(t *testing.T) {
	validator := &LLMPluginValidator{}

	config := makeLLMService("ollama", "", "")

	issues := validator.Validate(config)
	assert.Len(t, issues, 1)
	assert.Equal(t, parser.LevelError, issues[0].Level)
	assert.Contains(t, issues[0].Message, "requires llmModel")
}

func TestLLMPluginValidator_OpenAIEmptySecretKey(t *testing.T) {
	t.Setenv("OPEN_AI_SECRET_KEY", "")
	validator := &LLMPluginValidator{}

	config := makeLLMService("openai", "gpt-4o", "")

	issues := validator.Validate(config)
	assert.Len(t, issues, 1)
	assert.Equal(t, parser.LevelWarning, issues[0].Level)
	assert.Contains(t, issues[0].Message, "openAISecretKey is empty")
}

func TestLLMPluginValidator_OpenAISecretKeyFromEnv(t *testing.T) {
	t.Setenv("OPEN_AI_SECRET_KEY", "test-only-key")
	issues := (&LLMPluginValidator{}).Validate(makeLLMService("openai", "gpt-4o", ""))
	assert.Empty(t, issues)
	assert.Equal(t, "test-only-key", os.Getenv("OPEN_AI_SECRET_KEY"))
}

func TestLLMPluginValidator_OllamaEmptySecretKey(t *testing.T) {
	validator := &LLMPluginValidator{}

	config := makeLLMService("ollama", "llama3", "")

	issues := validator.Validate(config)
	assert.Empty(t, issues)
}

func TestLLMPluginValidator_FallbackCommand(t *testing.T) {
	validator := &LLMPluginValidator{}

	config := parser.BeelzebubServiceConfiguration{
		FallbackCommand: parser.Command{
			Plugin: LLMPluginName,
		},
		Plugin: parser.Plugin{
			LLMProvider: "",
			LLMModel:    "",
		},
	}

	issues := validator.Validate(config)
	assert.Len(t, issues, 2)
	assert.Equal(t, parser.LevelError, issues[0].Level)
	assert.Contains(t, issues[0].Message, "requires llmProvider")
	assert.Equal(t, parser.LevelError, issues[1].Level)
	assert.Contains(t, issues[1].Message, "requires llmModel")
}
