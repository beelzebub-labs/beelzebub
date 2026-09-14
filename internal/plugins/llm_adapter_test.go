package plugins

import (
	"context"
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
	"github.com/stretchr/testify/assert"
)

func TestLLMPlugin_Metadata(t *testing.T) {
	lp := &llmPlugin{}
	meta := lp.Metadata()

	assert.Equal(t, LLMPluginName, meta.Name)
	assert.NotEmpty(t, meta.Version)
	assert.NotEmpty(t, meta.Description)
}

func TestLLMPlugin_Execute_UnknownProvider(t *testing.T) {
	lp := &llmPlugin{}

	_, err := lp.Execute(context.Background(), plugin.CommandRequest{
		Command:  "ls",
		ClientIP: "127.0.0.1",
		Protocol: "ssh",
		Config: plugin.Config{
			LLMProvider: "unknown-provider",
		},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "llm plugin")
}

func TestLLMPlugin_Execute_UnknownProtocol(t *testing.T) {
	lp := &llmPlugin{}

	_, err := lp.Execute(context.Background(), plugin.CommandRequest{
		Command:  "ls",
		ClientIP: "127.0.0.1",
		Protocol: "ftp", // not a known protocol
		Config: plugin.Config{
			LLMProvider: "openai",
		},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown protocol")
}

func TestLLMPlugin_Execute_OllamaConnectionError(t *testing.T) {
	lp := &llmPlugin{}

	_, err := lp.Execute(context.Background(), plugin.CommandRequest{
		Command:  "ls",
		ClientIP: "127.0.0.1",
		Protocol: "ssh",
		Config: plugin.Config{
			LLMProvider: "ollama",
			LLMModel:    "test",
			Host:        "http://127.0.0.1:1",
		},
	})

	assert.Error(t, err)
}
