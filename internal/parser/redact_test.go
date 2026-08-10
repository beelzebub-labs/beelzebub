package parser

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

const fakeSecret = "sk-proj-000NOTAREALKEY111DEADBEEF222"

// See issue #349: debug logging formats the whole service configuration.
func TestPluginStringRedactsOpenAISecretKey(t *testing.T) {
	plugin := Plugin{
		OpenAISecretKey: fakeSecret,
		LLMModel:        "gpt-4o",
		LLMProvider:     "openai",
	}

	printed := fmt.Sprintf("%v", plugin)

	assert.NotContains(t, printed, fakeSecret)
	assert.Contains(t, printed, redactedPlaceholder)
	assert.Contains(t, printed, "gpt-4o")
	assert.Contains(t, printed, "openai")
}

func TestPluginStringKeepsEmptySecretEmpty(t *testing.T) {
	printed := fmt.Sprintf("%v", Plugin{LLMModel: "gpt-4o"})

	// An unset key is not a secret.
	assert.NotContains(t, printed, redactedPlaceholder)
	assert.Contains(t, printed, "gpt-4o")
}

// Redaction must survive the parent struct and a pointer to it.
func TestServiceConfigurationStringRedactsNestedSecret(t *testing.T) {
	config := &BeelzebubServiceConfiguration{
		Protocol:    "http",
		Address:     ":8080",
		Description: "LLM lure",
		Plugin:      Plugin{OpenAISecretKey: fakeSecret},
	}

	for _, format := range []string{"%v", "%+v", "%s"} {
		printed := fmt.Sprintf(format, config)
		assert.NotContains(t, printed, fakeSecret, "leaked with %s", format)
		assert.Contains(t, printed, "LLM lure", "dropped context with %s", format)
	}
}

// RawConfig keeps the original parsed document. This is the path that leaked.
func TestServiceConfigurationStringRedactsRawConfig(t *testing.T) {
	config := &BeelzebubServiceConfiguration{
		Protocol: "http",
		RawConfig: map[string]any{
			"protocol": "http",
			"plugin": map[string]any{
				"openAISecretKey": fakeSecret,
				"llmModel":        "gpt-4o",
			},
		},
	}

	printed := fmt.Sprintf("%v", config)

	assert.NotContains(t, printed, fakeSecret)
	assert.Contains(t, printed, "gpt-4o")
}

// Formatting must not mutate the configuration being served.
func TestRawConfigRedactionDoesNotMutateOriginal(t *testing.T) {
	plugin := map[string]any{"openAISecretKey": fakeSecret}
	config := &BeelzebubServiceConfiguration{
		RawConfig: map[string]any{"plugin": plugin},
	}

	_ = fmt.Sprintf("%v", config)

	assert.Equal(t, fakeSecret, plugin["openAISecretKey"])
}

// passwordRegex is not a secret; key matching must be exact, not substring.
func TestRawConfigKeepsNonSecretLookalikeKeys(t *testing.T) {
	config := &BeelzebubServiceConfiguration{
		RawConfig: map[string]any{"passwordRegex": "^(root|admin)$"},
	}

	assert.Contains(t, fmt.Sprintf("%v", config), "^(root|admin)$")
}

// Real documents nest secrets under lists, so the walk must descend slices.
func TestRawConfigRedactsSecretNestedInList(t *testing.T) {
	config := &BeelzebubServiceConfiguration{
		RawConfig: map[string]any{
			"services": []any{
				map[string]any{"plugin": map[string]any{"openAISecretKey": fakeSecret}},
				map[string]any{"description": "second service"},
			},
		},
	}

	printed := fmt.Sprintf("%v", config)

	assert.NotContains(t, printed, fakeSecret)
	assert.Contains(t, printed, "second service")
}

// Configuration supplied as JSON can hold a non-string scalar.
func TestRawConfigRedactsNonStringSecret(t *testing.T) {
	config := &BeelzebubServiceConfiguration{
		RawConfig: map[string]any{
			"plugin": map[string]any{"openAISecretKey": 1234567890},
		},
	}

	printed := fmt.Sprintf("%v", config)

	assert.NotContains(t, printed, "1234567890")
	assert.Contains(t, printed, redactedPlaceholder)
}

// A null key is not configured, so it must not read as a secret.
func TestRawConfigLeavesNullSecretAlone(t *testing.T) {
	config := &BeelzebubServiceConfiguration{
		RawConfig: map[string]any{
			"plugin": map[string]any{"openAISecretKey": nil},
		},
	}

	assert.NotContains(t, fmt.Sprintf("%v", config), redactedPlaceholder)
}

func TestBeelzebubCloudStringRedactsAuthToken(t *testing.T) {
	const token = "bzb-cloud-000NOTAREALTOKEN111"

	printed := fmt.Sprintf("%v", BeelzebubCloud{
		Enabled:   true,
		URI:       "https://cloud.example/events",
		AuthToken: token,
	})

	assert.NotContains(t, printed, token)
	assert.Contains(t, printed, redactedPlaceholder)
	assert.Contains(t, printed, "https://cloud.example/events")
}

// Redaction must not reach the JSON encoder that HashCode uses.
func TestHashCodeIsUnaffectedByRedaction(t *testing.T) {
	withSecret := BeelzebubServiceConfiguration{
		Protocol: "http", Address: ":8080",
		Plugin: Plugin{OpenAISecretKey: fakeSecret},
	}
	withoutSecret := BeelzebubServiceConfiguration{
		Protocol: "http", Address: ":8080",
		Plugin: Plugin{OpenAISecretKey: "a-different-key"},
	}

	first, err := withSecret.HashCode()
	assert.Nil(t, err)
	second, err := withoutSecret.HashCode()
	assert.Nil(t, err)

	assert.NotEqual(t, first, second, "HashCode must still see the real key")
}
