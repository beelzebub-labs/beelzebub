package plugins

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/go-resty/resty/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOrcaRouterLiveCatalogAndCompletion exercises the real OrcaRouter endpoint
// through the provider path. It is skipped unless ORCAROUTER_API_KEY is set, so
// the offline test suite never depends on network access or a credential.
func TestOrcaRouterLiveCatalogAndCompletion(t *testing.T) {
	apiKey := os.Getenv(orcaRouterKeyEnv)
	if apiKey == "" {
		t.Skip("set ORCAROUTER_API_KEY to run the live OrcaRouter checks")
	}

	client := resty.New()
	ResetOrcaRouterCatalogCache()
	t.Cleanup(ResetOrcaRouterCatalogCache)

	catalog := fetchOrcaRouterCatalog(context.Background(), client, OrcaRouterModelsURL(orcaRouterChatEndpoint), apiKey)
	require.Equal(t, "live", catalog.Source, "a live catalog fetch must return the live catalog")
	require.NotZero(t, catalog.Len())
	assert.Contains(t, catalog.IDs(OrcaRouterCapabilityChat), "deepseek/deepseek-v4-pro")

	hp := InitLLMHoneypot(LLMHoneypot{
		Histories: make([]Message, 0),
		Protocol:  tracer.SSH,
		Model:     "deepseek/deepseek-v4-pro",
		Provider:  OrcaRouter,
	})
	require.NotEmpty(t, hp.OrcaRouterKey)

	// A live gateway can occasionally return an empty completion for a
	// reasoning model; retry so the check proves the wiring instead of
	// depending on one sampled response.
	commands := []string{"ls", "ls -la", "pwd"}
	var answer string
	var err error
	for _, command := range commands {
		answer, err = hp.ExecuteModel(command, "127.0.0.1")
		if err == nil && strings.TrimSpace(answer) != "" {
			break
		}
	}

	require.NoError(t, err)
	assert.NotEmpty(t, strings.TrimSpace(answer))
}
