package parser

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

const fakeSecret = "sk-proj-000NOTAREALKEY111DEADBEEF222"

// Debug logging formats the whole service configuration with the fmt verbs, so
// every secret-bearing struct must redact itself when printed. See issue #349.
func TestPluginStringRedactsOpenAISecretKey(t *testing.T) {
	plugin := Plugin{
		OpenAISecretKey: fakeSecret,
		LLMModel:        "gpt-4o",
		LLMProvider:     "openai",
	}

	printed := fmt.Sprintf("%v", plugin)

	assert.NotContains(t, printed, fakeSecret)
	assert.Contains(t, printed, redactedPlaceholder)
	// Non-sensitive fields must survive, or debug logging loses its purpose.
	assert.Contains(t, printed, "gpt-4o")
	assert.Contains(t, printed, "openai")
}

func TestPluginStringKeepsEmptySecretEmpty(t *testing.T) {
	printed := fmt.Sprintf("%v", Plugin{LLMModel: "gpt-4o"})

	// An unset key is not a secret; printing [REDACTED] would be misleading.
	assert.NotContains(t, printed, redactedPlaceholder)
	assert.Contains(t, printed, "gpt-4o")
}

// The leak in #349 is a whole-config dump, so redaction has to survive being
// reached through the parent struct and through a pointer to it.
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

// RawConfig keeps the original parsed document, so the secret survives there
// even after the typed field is redacted. This is the path that actually leaked.
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

// Redacting the printed copy must not mutate the configuration itself, or the
// second log line would print a configuration whose key has been destroyed.
func TestRawConfigRedactionDoesNotMutateOriginal(t *testing.T) {
	plugin := map[string]any{"openAISecretKey": fakeSecret}
	config := &BeelzebubServiceConfiguration{
		RawConfig: map[string]any{"plugin": plugin},
	}

	_ = fmt.Sprintf("%v", config)

	assert.Equal(t, fakeSecret, plugin["openAISecretKey"])
}

// passwordRegex is not a secret. Over-redaction would make debug logs useless
// for the SSH honeypot, so key matching must be exact rather than substring.
func TestRawConfigKeepsNonSecretLookalikeKeys(t *testing.T) {
	config := &BeelzebubServiceConfiguration{
		RawConfig: map[string]any{"passwordRegex": "^(root|admin)$"},
	}

	assert.Contains(t, fmt.Sprintf("%v", config), "^(root|admin)$")
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

// HashCode feeds config-drift detection, so redaction must not reach the JSON
// encoder: a redacted hash would change on every deployment that sets a key.
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
