package plugins

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/go-resty/resty/v2"
	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadCatalogFixture(t *testing.T) *OrcaRouterCatalog {
	t.Helper()
	body, err := os.ReadFile("testdata/orcarouter-models.json")
	require.NoError(t, err)
	catalog, err := parseOrcaRouterModels(body)
	require.NoError(t, err)
	return catalog
}

func TestOrcaRouterCatalogParsesCapabilities(t *testing.T) {
	catalog := loadCatalogFixture(t)

	assert.NotZero(t, catalog.Len())
	assert.Equal(t, "live", catalog.Source)

	textOnly, ok := catalog.Lookup("deepseek/deepseek-v4-pro")
	require.True(t, ok)
	assert.True(t, textOnly.SupportsChat())
	assert.False(t, textOnly.SupportsImageInput(), "text-only model must not accept image input")

	imageModel, ok := catalog.Lookup("openai/gpt-5.5")
	require.True(t, ok)
	assert.True(t, imageModel.SupportsChat())
	assert.True(t, imageModel.SupportsImageInput())
	assert.Contains(t, imageModel.SupportedParameters, "reasoning", "catalog metadata must be preserved")
	assert.Equal(t, 128000, *imageModel.MaxCompletionTokens)
}

func TestOrcaRouterCatalogCapabilityFilters(t *testing.T) {
	catalog := loadCatalogFixture(t)

	assert.Contains(t, catalog.IDs(OrcaRouterCapabilityChat), "deepseek/deepseek-v4-pro")
	assert.NotContains(t, catalog.IDs(OrcaRouterCapabilityChat), "openai/text-embedding-3-large",
		"embedding-only models must not be offered for chat")

	assert.Equal(t, []string{"openai/text-embedding-3-large"}, catalog.IDs(OrcaRouterCapabilityEmbedding))
	assert.ElementsMatch(t, []string{"openai/gpt-image-1", "google/imagen-4.0-generate-001"}, catalog.IDs(OrcaRouterCapabilityImageGen))
	assert.Equal(t, []string{"kling/kling-3-turbo"}, catalog.IDs(OrcaRouterCapabilityVideo))
	assert.Equal(t, []string{"fixture/rerank-model"}, catalog.IDs(OrcaRouterCapabilityRerank))

	imageInput := catalog.IDs(OrcaRouterCapabilityImageIn)
	assert.Contains(t, imageInput, "openai/gpt-5.5")
	assert.NotContains(t, imageInput, "deepseek/deepseek-v4-pro")
	assert.NotContains(t, imageInput, "google/imagen-4.0-generate-001", "image generation is not image-input chat")
}

func TestOrcaRouterModelSupportsChatFailClosed(t *testing.T) {
	embedding := OrcaRouterModel{ID: "x", SupportedEndpointTypes: []string{"embeddings"}}
	assert.False(t, embedding.SupportsChat())

	unknown := OrcaRouterModel{ID: "y"}
	assert.True(t, unknown.SupportsChat(), "a model with no declared endpoints is left to the provider")

	responses := OrcaRouterModel{ID: "z", EndpointCapabilities: []string{"openai_responses"}}
	assert.True(t, responses.SupportsChat())
}

func TestOrcaRouterModelsURL(t *testing.T) {
	assert.Equal(t, "https://api.orcarouter.ai/v1/models", OrcaRouterModelsURL(""))
	assert.Equal(t, "https://api.orcarouter.ai/v1/models", OrcaRouterModelsURL(orcaRouterChatEndpoint))
	assert.Equal(t, "https://api.orcarouter.ai/v1/models", OrcaRouterModelsURL("https://api.orcarouter.ai/v1/chat/completions/"))
	assert.Equal(t, "https://proxy.internal/v1/models", OrcaRouterModelsURL("https://proxy.internal/v1/chat/completions"))
}

func TestSeedOrcaRouterCatalog(t *testing.T) {
	seed := SeedOrcaRouterCatalog()
	assert.Equal(t, "seed", seed.Source)

	for _, id := range []string{
		"openai/gpt-5.5",
		"anthropic/claude-opus-4.8",
		"google/gemini-3.5-flash",
		"deepseek/deepseek-v4-pro",
		"orcarouter/auto",
	} {
		_, ok := seed.Lookup(id)
		assert.True(t, ok, "seed must contain verified model %s", id)
	}

	assert.Contains(t, seed.IDs(OrcaRouterCapabilityChat), "orcarouter/fusion",
		"documented fallback model must be available when the catalog is unreachable")
}

func TestFromStringToLLMProviderOrcaRouter(t *testing.T) {
	provider, err := FromStringToLLMProvider("orcarouter")
	assert.NoError(t, err)
	assert.Equal(t, OrcaRouter, provider)

	provider, err = FromStringToLLMProvider("OrcaRouter")
	assert.NoError(t, err)
	assert.Equal(t, OrcaRouter, provider)

	_, err = FromStringToLLMProvider("beelzebub-model")
	assert.ErrorContains(t, err, "valid providers: ollama, openai, orcarouter")
}

func TestInitLLMHoneypotOrcaRouterKeyFromEnv(t *testing.T) {
	t.Setenv(orcaRouterKeyEnv, "sk-orca-test-only")

	hp := InitLLMHoneypot(LLMHoneypot{
		Histories: make([]Message, 0),
		Protocol:  tracer.SSH,
		Model:     orcaRouterDefaultModel,
		Provider:  OrcaRouter,
	})

	assert.Equal(t, "sk-orca-test-only", hp.OrcaRouterKey)
}

func TestOrcaRouterCallerRequiresKey(t *testing.T) {
	t.Setenv(orcaRouterKeyEnv, "")

	hp := InitLLMHoneypot(LLMHoneypot{
		Histories: make([]Message, 0),
		Protocol:  tracer.SSH,
		Model:     orcaRouterDefaultModel,
		Provider:  OrcaRouter,
	})

	_, err := hp.ExecuteModel("ls", "127.0.0.1")
	assert.ErrorContains(t, err, "orcaRouterKey is empty: set ORCAROUTER_API_KEY")
}

func newOrcaRouterMockHoneypot(t *testing.T, catalog *OrcaRouterCatalog) (*LLMHoneypot, *resty.Client) {
	t.Helper()
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	t.Cleanup(httpmock.DeactivateAndReset)
	t.Setenv(orcaRouterKeyEnv, "sk-orca-test-only")

	hp := InitLLMHoneypot(LLMHoneypot{
		Histories:     make([]Message, 0),
		OrcaRouterKey: "sk-orca-test-only",
		Protocol:      tracer.SSH,
		Host:          orcaRouterChatEndpoint,
		Model:         "deepseek/deepseek-v4-pro",
		Provider:      OrcaRouter,
		catalog:       catalog,
	})
	hp.client = client
	return hp, client
}

func TestOrcaRouterCallerPostsChatCompletion(t *testing.T) {
	hp, _ := newOrcaRouterMockHoneypot(t, loadCatalogFixture(t))

	var captured Request
	var authHeader string
	httpmock.RegisterResponder("POST", orcaRouterChatEndpoint, func(req *http.Request) (*http.Response, error) {
		authHeader = req.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(req.Body).Decode(&captured))
		return httpmock.NewJsonResponse(200, &Response{
			Choices: []Choice{{Message: Message{Role: ASSISTANT.String(), Content: "```\nprova.txt\n```"}}},
		})
	})

	result, err := hp.ExecuteModel("ls", "127.0.0.1")

	require.NoError(t, err)
	assert.Equal(t, "prova.txt", strings.TrimSpace(result), "code fences are stripped from the response")
	assert.Equal(t, "deepseek/deepseek-v4-pro", captured.Model)
	assert.False(t, captured.Stream)
	assert.Equal(t, "Bearer sk-orca-test-only", authHeader)
	assert.NotEmpty(t, captured.Messages)
	assert.Equal(t, systemPromptVirtualizeLinuxTerminal, captured.Messages[0].Content)
}

func TestOrcaRouterCallerUsesConfiguredHost(t *testing.T) {
	hp, _ := newOrcaRouterMockHoneypot(t, loadCatalogFixture(t))
	hp.Host = "https://orcarouter-proxy.internal/v1/chat/completions"

	httpmock.RegisterResponder("POST", hp.Host, func(req *http.Request) (*http.Response, error) {
		return httpmock.NewJsonResponse(200, &Response{
			Choices: []Choice{{Message: Message{Role: ASSISTANT.String(), Content: "ok"}}},
		})
	})

	result, err := hp.ExecuteModel("pwd", "127.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
}

func TestOrcaRouterCallerRejectsNonChatModel(t *testing.T) {
	hp, client := newOrcaRouterMockHoneypot(t, loadCatalogFixture(t))
	hp.Model = "openai/text-embedding-3-large"

	calls := 0
	httpmock.RegisterResponder("POST", orcaRouterChatEndpoint, func(req *http.Request) (*http.Response, error) {
		calls++
		return httpmock.NewJsonResponse(200, &Response{})
	})

	_, err := hp.ExecuteModel("ls", "127.0.0.1")

	require.Error(t, err)
	assert.Contains(t, err.Error(), `does not support chat completions`)
	assert.Contains(t, err.Error(), "orcacyber-zero")
	assert.Equal(t, 0, calls, "a non-chat model must fail before any inference request is spent")
	assert.NotNil(t, client)
}

func TestOrcaRouterCallerAllowsUnknownModel(t *testing.T) {
	hp, _ := newOrcaRouterMockHoneypot(t, loadCatalogFixture(t))
	hp.Model = "vendor/new-model-not-in-catalog"

	httpmock.RegisterResponder("POST", orcaRouterChatEndpoint, func(req *http.Request) (*http.Response, error) {
		return httpmock.NewJsonResponse(200, &Response{
			Choices: []Choice{{Message: Message{Role: ASSISTANT.String(), Content: "ok"}}},
		})
	})

	result, err := hp.ExecuteModel("ls", "127.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
}

func TestOrcaRouterCallerErrorSemantics(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		expected string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`, "rejected the API key (401)"},
		{"forbidden", http.StatusForbidden, `{"error":{"code":"model_access_denied"}}`, "denied access to this model (403)"},
		{"rate limited", http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`, "rate limited the request (429)"},
		{"server error", http.StatusInternalServerError, `{"error":{"message":"boom"}}`, "returned HTTP 500"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hp, _ := newOrcaRouterMockHoneypot(t, loadCatalogFixture(t))
			httpmock.RegisterResponder("POST", orcaRouterChatEndpoint,
				httpmock.NewStringResponder(tt.status, tt.body))

			_, err := hp.ExecuteModel("ls", "127.0.0.1")

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expected)
			assert.NotContains(t, err.Error(), "sk-orca-test-only", "errors must never echo the credential")
		})
	}
}

func TestOrcaRouterCallerNetworkError(t *testing.T) {
	hp, _ := newOrcaRouterMockHoneypot(t, loadCatalogFixture(t))
	httpmock.RegisterResponder("POST", orcaRouterChatEndpoint,
		func(req *http.Request) (*http.Response, error) {
			return nil, assert.AnError
		})

	_, err := hp.ExecuteModel("ls", "127.0.0.1")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "orcarouter request failed")
}

func TestOrcaRouterCallerNoChoices(t *testing.T) {
	hp, _ := newOrcaRouterMockHoneypot(t, loadCatalogFixture(t))
	httpmock.RegisterResponder("POST", orcaRouterChatEndpoint,
		httpmock.NewJsonResponderOrPanic(200, &Response{}))

	_, err := hp.ExecuteModel("ls", "127.0.0.1")
	assert.EqualError(t, err, "no choices")
}

func TestOrcaRouterCatalogDiscoveryUsesLiveResponse(t *testing.T) {
	ResetOrcaRouterCatalogCache()
	t.Cleanup(ResetOrcaRouterCatalogCache)

	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	httpmock.RegisterResponder("GET", orcaRouterBaseURL+"/models", func(req *http.Request) (*http.Response, error) {
		assert.Equal(t, "Bearer sk-orca-test-only", req.Header.Get("Authorization"))
		body, err := os.ReadFile("testdata/orcarouter-models.json")
		require.NoError(t, err)
		response := httpmock.NewStringResponse(200, string(body))
		response.Header.Set("Content-Type", "application/json")
		return response, nil
	})

	catalog := fetchOrcaRouterCatalog(t.Context(), client, orcaRouterBaseURL+"/models", "sk-orca-test-only")

	assert.Equal(t, "live", catalog.Source)
	_, ok := catalog.Lookup("openai/text-embedding-3-large")
	assert.True(t, ok)
}

func TestOrcaRouterCatalogDiscoveryFallsBackToSeed(t *testing.T) {
	tests := []struct {
		name    string
		respond httpmock.Responder
	}{
		{"network error", func(req *http.Request) (*http.Response, error) { return nil, assert.AnError }},
		{"server error", httpmock.NewStringResponder(http.StatusBadGateway, "bad gateway")},
		{"empty catalog", httpmock.NewStringResponder(200, `{"object":"list","data":[]}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ResetOrcaRouterCatalogCache()
			t.Cleanup(ResetOrcaRouterCatalogCache)

			client := resty.New()
			httpmock.ActivateNonDefault(client.GetClient())
			defer httpmock.DeactivateAndReset()

			httpmock.RegisterResponder("GET", orcaRouterBaseURL+"/models", tt.respond)

			catalog := fetchOrcaRouterCatalog(t.Context(), client, orcaRouterBaseURL+"/models", "sk-orca-test-only")

			assert.Equal(t, "seed", catalog.Source)
			_, ok := catalog.Lookup("openai/gpt-5.5")
			assert.True(t, ok, "verified seed keeps the provider usable during a catalog outage")
		})
	}
}

func TestOrcaRouterCatalogCacheIsReused(t *testing.T) {
	ResetOrcaRouterCatalogCache()
	t.Cleanup(ResetOrcaRouterCatalogCache)

	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	calls := 0
	httpmock.RegisterResponder("GET", orcaRouterBaseURL+"/models", func(req *http.Request) (*http.Response, error) {
		calls++
		return httpmock.NewStringResponse(200, `{"data":[{"id":"orcarouter/fusion","supported_endpoint_types":["openai"]}]}`), nil
	})

	fetchOrcaRouterCatalog(t.Context(), client, orcaRouterBaseURL+"/models", "sk-orca-test-only")
	fetchOrcaRouterCatalog(t.Context(), client, orcaRouterBaseURL+"/models", "sk-orca-test-only")

	assert.Equal(t, 1, calls, "discovery must be bounded and cached")
}

func TestOrcaRouterHTTPErrorHandlesEmptyResponse(t *testing.T) {
	assert.NoError(t, orcaRouterHTTPError(200, nil))
	assert.EqualError(t, orcaRouterHTTPError(0, nil), "orcarouter returned an empty response")
	assert.Contains(t, orcaRouterHTTPError(500, []byte("boom")).Error(), "boom")
}
