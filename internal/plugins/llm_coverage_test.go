package plugins

import (
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/stretchr/testify/require"
)

func TestLLMHoneypot_ValidationPromptsRejectUnknownProtocol(t *testing.T) {
	honeypot := LLMHoneypot{Protocol: tracer.TCP}
	_, err := honeypot.buildInputValidationPrompt("command")
	require.Error(t, err)

	honeypot.Protocol = tracer.MCP
	_, err = honeypot.buildOutputValidationPrompt("output")
	require.Error(t, err)
}
