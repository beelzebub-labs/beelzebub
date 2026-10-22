package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

// OrcaRouter model catalog discovery.
//
// OrcaRouter exposes the same catalog it routes to at GET {origin}/v1/models.
// The catalog is used to fail closed before spending a request on a model that
// cannot serve chat completions, and to describe input modalities so an
// image-only or embedding-only model is never silently used for a terminal
// decoy. Discovery is bounded: one request with a short timeout, cached for a
// fixed window, and backed by a verified seed when the catalog is unreachable.

const (
	// orcaRouterDefaultModel is the documented recommended default for this
	// deception runtime: OrcaCyber Zero, the OrcaRouter security-specialised
	// model. It requires a business-verified workspace to unlock.
	orcaRouterDefaultModel = "orcacyber-zero"
	// orcaRouterFallbackModel is the documented fallback when OrcaCyber Zero is
	// not unlocked for the workspace that owns the API key.
	orcaRouterFallbackModel = "orcarouter/fusion"

	orcaRouterCatalogTTL     = 10 * time.Minute
	orcaRouterCatalogTimeout = 10 * time.Second
	orcaRouterMaxBodySnippet = 200
)

// OrcaRouterCapability names the capability filters the catalog supports.
type OrcaRouterCapability string

const (
	OrcaRouterCapabilityChat      OrcaRouterCapability = "chat"
	OrcaRouterCapabilityImageIn   OrcaRouterCapability = "image-input"
	OrcaRouterCapabilityEmbedding OrcaRouterCapability = "embedding"
	OrcaRouterCapabilityImageGen  OrcaRouterCapability = "image-generation"
	OrcaRouterCapabilityVideo     OrcaRouterCapability = "video"
	OrcaRouterCapabilityRerank    OrcaRouterCapability = "rerank"
)

// OrcaRouterArchitecture describes the modalities a model accepts and returns.
type OrcaRouterArchitecture struct {
	Modality         string   `json:"modality,omitempty"`
	InputModalities  []string `json:"input_modalities,omitempty"`
	OutputModalities []string `json:"output_modalities,omitempty"`
}

// OrcaRouterModel is one entry of the OrcaRouter catalog.
type OrcaRouterModel struct {
	ID                     string                 `json:"id"`
	Object                 string                 `json:"object,omitempty"`
	OwnedBy                string                 `json:"owned_by,omitempty"`
	Name                   string                 `json:"name,omitempty"`
	SupportedEndpointTypes []string               `json:"supported_endpoint_types,omitempty"`
	EndpointCapabilities   []string               `json:"endpoint_capabilities,omitempty"`
	Architecture           OrcaRouterArchitecture `json:"architecture,omitempty"`
	ContextLength          *int                   `json:"context_length,omitempty"`
	MaxCompletionTokens    *int                   `json:"max_completion_tokens,omitempty"`
	SupportedParameters    []string               `json:"supported_parameters,omitempty"`
}

// InputModality reports whether the catalog lists a modality as accepted.
// An empty architecture is treated as text-only, which keeps the check
// fail-closed for multimodal input.
func (m OrcaRouterModel) InputModality(modality string) bool {
	for _, candidate := range m.Architecture.InputModalities {
		if strings.EqualFold(candidate, modality) {
			return true
		}
	}
	return false
}

// SupportsChat reports whether the model can serve an OpenAI-compatible chat
// completion. Models whose catalog entry lists no endpoints at all are unknown
// and left to the provider to accept or reject.
func (m OrcaRouterModel) SupportsChat() bool {
	if len(m.SupportedEndpointTypes) == 0 && len(m.EndpointCapabilities) == 0 {
		return true
	}

	for _, capability := range m.EndpointCapabilities {
		switch capability {
		case "chat_completions", "openai_responses", "anthropic_messages", "gemini_generate":
			return true
		}
	}

	for _, endpoint := range m.SupportedEndpointTypes {
		switch endpoint {
		case "openai", "openai-response", "anthropic", "gemini":
			return true
		}
	}

	return false
}

// SupportsImageInput reports whether the model accepts image input. It is
// fail-closed: models with no declared input modalities count as text-only.
func (m OrcaRouterModel) SupportsImageInput() bool {
	return m.InputModality("image")
}

// OrcaRouterCatalog is an immutable snapshot of the OrcaRouter model catalog.
type OrcaRouterCatalog struct {
	Models    map[string]OrcaRouterModel
	FetchedAt time.Time
	Source    string
}

// NewOrcaRouterCatalog indexes a catalog response.
func NewOrcaRouterCatalog(models []OrcaRouterModel) *OrcaRouterCatalog {
	indexed := make(map[string]OrcaRouterModel, len(models))
	for _, model := range models {
		if model.ID == "" {
			continue
		}
		indexed[model.ID] = model
	}
	return &OrcaRouterCatalog{
		Models:    indexed,
		FetchedAt: time.Now(),
		Source:    "live",
	}
}

// Len returns the number of catalog entries.
func (c *OrcaRouterCatalog) Len() int {
	if c == nil {
		return 0
	}
	return len(c.Models)
}

// Lookup returns the catalog entry for an exact model ID.
func (c *OrcaRouterCatalog) Lookup(modelID string) (OrcaRouterModel, bool) {
	if c == nil {
		return OrcaRouterModel{}, false
	}
	model, ok := c.Models[modelID]
	return model, ok
}

// IDs returns the sorted model IDs matching the capability filter.
func (c *OrcaRouterCatalog) IDs(capability OrcaRouterCapability) []string {
	if c == nil {
		return nil
	}
	ids := make([]string, 0, len(c.Models))
	for id, model := range c.Models {
		if model.Matches(capability) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// Matches applies a capability filter to one catalog entry.
func (m OrcaRouterModel) Matches(capability OrcaRouterCapability) bool {
	switch capability {
	case OrcaRouterCapabilityChat:
		return m.SupportsChat()
	case OrcaRouterCapabilityImageIn:
		return m.SupportsChat() && m.SupportsImageInput()
	case OrcaRouterCapabilityEmbedding:
		return hasEndpoint(m, "embeddings") || hasCapability(m, "embedding")
	case OrcaRouterCapabilityImageGen:
		return hasEndpoint(m, "image-generation") || hasCapability(m, "image_generation")
	case OrcaRouterCapabilityVideo:
		return hasEndpoint(m, "openai-video") || hasCapability(m, "video_generation")
	case OrcaRouterCapabilityRerank:
		return hasEndpoint(m, "rerank") || hasCapability(m, "rerank")
	default:
		return false
	}
}

func hasEndpoint(m OrcaRouterModel, endpoint string) bool {
	for _, candidate := range m.SupportedEndpointTypes {
		if candidate == endpoint {
			return true
		}
	}
	return false
}

func hasCapability(m OrcaRouterModel, capability string) bool {
	for _, candidate := range m.EndpointCapabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}

// SeedOrcaRouterCatalog is the verified fallback catalog. It carries only IDs
// observed in a live OrcaRouter catalog response, with the metadata needed to
// describe text-only and image-capable chat models. Model IDs verified against
// https://api.orcarouter.ai/v1/models on 2026-10-22.
func SeedOrcaRouterCatalog() *OrcaRouterCatalog {
	seed := NewOrcaRouterCatalog([]OrcaRouterModel{
		{
			ID:                     "openai/gpt-5.5",
			OwnedBy:                "OpenAI",
			SupportedEndpointTypes: []string{"openai", "openai-response"},
			EndpointCapabilities:   []string{"chat_completions", "openai_responses"},
			Architecture:           OrcaRouterArchitecture{InputModalities: []string{"file", "image", "text"}},
			SupportedParameters:    []string{"max_completion_tokens", "max_tokens", "reasoning", "response_format", "seed", "stream", "structured_outputs", "tool_choice", "tools"},
		},
		{
			ID:                     "anthropic/claude-opus-4.8",
			OwnedBy:                "Anthropic",
			SupportedEndpointTypes: []string{"openai", "anthropic", "openai-response"},
			EndpointCapabilities:   []string{"chat_completions", "anthropic_messages", "openai_responses"},
			Architecture:           OrcaRouterArchitecture{InputModalities: []string{"text", "image", "file"}, OutputModalities: []string{"text"}},
			SupportedParameters:    []string{"max_tokens", "reasoning", "response_format", "stop", "stream", "structured_outputs", "temperature", "tool_choice", "tools", "top_k", "top_p"},
		},
		{
			ID:                     "google/gemini-3.5-flash",
			OwnedBy:                "Google",
			SupportedEndpointTypes: []string{"openai", "gemini"},
			EndpointCapabilities:   []string{"chat_completions", "gemini_generate"},
			Architecture:           OrcaRouterArchitecture{InputModalities: []string{"text", "image", "video", "file", "audio"}, OutputModalities: []string{"text"}},
			SupportedParameters:    []string{"max_tokens", "reasoning", "response_format", "seed", "stop", "stream", "structured_outputs", "temperature", "tool_choice", "tools", "top_p"},
		},
		{
			ID:                     "deepseek/deepseek-v4-pro",
			OwnedBy:                "DeepSeek",
			SupportedEndpointTypes: []string{"openai", "openai-response"},
			EndpointCapabilities:   []string{"chat_completions", "openai_responses"},
			Architecture:           OrcaRouterArchitecture{InputModalities: []string{"text"}},
			SupportedParameters:    []string{"max_tokens", "reasoning", "response_format", "stop", "stream", "temperature", "tool_choice", "tools", "top_p"},
		},
		{
			ID:                     "orcarouter/auto",
			OwnedBy:                "orcarouter",
			SupportedEndpointTypes: []string{"openai", "openai-response", "anthropic", "gemini"},
			EndpointCapabilities:   []string{"chat_completions", "openai_responses"},
			Architecture:           OrcaRouterArchitecture{InputModalities: []string{"text"}},
		},
		{
			ID:                     "orcarouter/fusion",
			OwnedBy:                "orcarouter",
			SupportedEndpointTypes: []string{"openai", "openai-response", "anthropic", "gemini"},
			EndpointCapabilities:   []string{"chat_completions", "openai_responses"},
			Architecture:           OrcaRouterArchitecture{InputModalities: []string{"text"}},
		},
	})
	seed.Source = "seed"
	return seed
}

// OrcaRouterModelsURL derives the catalog URL from the configured chat
// endpoint, so discovery always talks to the same origin as inference.
func OrcaRouterModelsURL(chatEndpoint string) string {
	endpoint := strings.TrimSpace(chatEndpoint)
	if endpoint == "" {
		return orcaRouterBaseURL + "/models"
	}
	endpoint = strings.TrimRight(endpoint, "/")
	endpoint = strings.TrimSuffix(endpoint, "/chat/completions")
	return endpoint + "/models"
}

type orcaRouterModelsResponse struct {
	Data []OrcaRouterModel `json:"data"`
}

var (
	orcaRouterCacheMu sync.Mutex
	orcaRouterCache   = map[string]*OrcaRouterCatalog{}
)

// ResetOrcaRouterCatalogCache clears the discovery cache. Intended for tests.
func ResetOrcaRouterCatalogCache() {
	orcaRouterCacheMu.Lock()
	defer orcaRouterCacheMu.Unlock()
	orcaRouterCache = map[string]*OrcaRouterCatalog{}
}

// fetchOrcaRouterCatalog performs one bounded GET on the configured origin and
// caches the result. A failure returns the verified seed instead of an error so
// that a catalog outage never stops the runtime.
func fetchOrcaRouterCatalog(ctx context.Context, client *resty.Client, modelsURL, apiKey string) *OrcaRouterCatalog {
	if modelsURL == "" {
		modelsURL = orcaRouterBaseURL + "/models"
	}

	orcaRouterCacheMu.Lock()
	cached, ok := orcaRouterCache[modelsURL]
	orcaRouterCacheMu.Unlock()
	if ok && time.Since(cached.FetchedAt) < orcaRouterCatalogTTL {
		return cached
	}

	if client == nil {
		client = resty.New()
	}

	requestCtx, cancel := context.WithTimeout(ctx, orcaRouterCatalogTimeout)
	defer cancel()

	var body orcaRouterModelsResponse
	response, err := client.R().
		SetContext(requestCtx).
		SetHeader("Accept", "application/json").
		SetAuthToken(apiKey).
		SetResult(&body).
		Get(modelsURL)

	catalog := SeedOrcaRouterCatalog()
	if err == nil && response != nil && response.StatusCode() == http.StatusOK {
		if fetched := NewOrcaRouterCatalog(body.Data); fetched.Len() > 0 {
			catalog = fetched
		}
	} else {
		status := 0
		if response != nil {
			status = response.StatusCode()
		}
		log.WithFields(log.Fields{
			"models_url": modelsURL,
			"status":     status,
			"error":      err,
		}).Warn("orcarouter model catalog unavailable, using verified seed")
	}

	orcaRouterCacheMu.Lock()
	orcaRouterCache[modelsURL] = catalog
	orcaRouterCacheMu.Unlock()

	return catalog
}

// orcaRouterHTTPError converts a non-2xx OrcaRouter response into an actionable
// error. The body snippet is length-limited and never contains the credential.
func orcaRouterHTTPError(status int, body []byte) error {
	if status >= 200 && status < 300 {
		return nil
	}

	snippet := strings.TrimSpace(string(body))
	if len(snippet) > orcaRouterMaxBodySnippet {
		snippet = snippet[:orcaRouterMaxBodySnippet]
	}

	switch status {
	case http.StatusUnauthorized:
		return fmt.Errorf("orcarouter rejected the API key (401): set a valid %s; %s", orcaRouterKeyEnv, snippet)
	case http.StatusForbidden:
		return fmt.Errorf("orcarouter denied access to this model (403): the workspace may need business verification to unlock it; %s", snippet)
	case http.StatusTooManyRequests:
		return fmt.Errorf("orcarouter rate limited the request (429); %s", snippet)
	default:
		if status == 0 {
			return errors.New("orcarouter returned an empty response")
		}
		return fmt.Errorf("orcarouter returned HTTP %d; %s", status, snippet)
	}
}

// parseOrcaRouterModels is a small helper used by tests and callers that already
// hold a catalog body.
func parseOrcaRouterModels(body []byte) (*OrcaRouterCatalog, error) {
	var payload orcaRouterModelsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decoding orcarouter models response: %w", err)
	}
	if len(payload.Data) == 0 {
		return nil, errors.New("orcarouter models response contained no models")
	}
	return NewOrcaRouterCatalog(payload.Data), nil
}
