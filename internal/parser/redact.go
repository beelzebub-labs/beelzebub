package parser

import (
	"fmt"
	"strings"
)

const redactedPlaceholder = "[REDACTED]"

// redactSecret keeps the distinction between "configured" and "not configured".
func redactSecret(value string) string {
	if value == "" {
		return ""
	}
	return redactedPlaceholder
}

// Format redacts the OpenAI secret key whenever a Plugin is printed.
//
// fmt.Formatter rather than fmt.Stringer so "%+v" keeps printing field names.
// MarshalJSON is deliberately left alone: HashCode marshals the configuration
// to detect drift, and redacting there would make every secret-bearing config
// hash alike.
func (plugin Plugin) Format(state fmt.State, verb rune) {
	type plain Plugin

	redacted := plain(plugin)
	redacted.OpenAISecretKey = redactSecret(redacted.OpenAISecretKey)

	fmt.Fprintf(state, fmt.FormatString(state, verb), redacted)
}

// sensitiveConfigKeys is matched exactly, not by substring: "passwordRegex" is
// not a secret. New secret-bearing fields must be added here.
var sensitiveConfigKeys = map[string]struct{}{
	"openaisecretkey": {},
	"auth-token":      {},
	"authtoken":       {},
}

// Format redacts secrets whenever a service configuration is printed. The typed
// Plugin field is handled by Plugin.Format; RawConfig holds the original parsed
// document and would otherwise reprint every secret verbatim.
func (config BeelzebubServiceConfiguration) Format(state fmt.State, verb rune) {
	type plain BeelzebubServiceConfiguration

	redacted := plain(config)
	redacted.RawConfig = redactRawConfig(config.RawConfig)

	fmt.Fprintf(state, fmt.FormatString(state, verb), redacted)
}

// redactSensitiveValue hides a secret whatever its type: configuration supplied
// as JSON can hold a non-string scalar. Null means "not configured".
func redactSensitiveValue(value any) any {
	if value == nil {
		return nil
	}
	if text, ok := value.(string); ok {
		return redactSecret(text)
	}
	return redactedPlaceholder
}

// redactRawConfig copies rather than edits in place: the caller is only
// formatting the configuration, and must not damage the one being served.
func redactRawConfig(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, nested := range typed {
			if _, sensitive := sensitiveConfigKeys[strings.ToLower(key)]; sensitive {
				result[key] = redactSensitiveValue(nested)
				continue
			}
			result[key] = redactRawConfig(nested)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, nested := range typed {
			result[i] = redactRawConfig(nested)
		}
		return result
	default:
		return value
	}
}

// Format redacts the cloud auth token whenever a BeelzebubCloud is printed.
func (cloud BeelzebubCloud) Format(state fmt.State, verb rune) {
	type plain BeelzebubCloud

	redacted := plain(cloud)
	redacted.AuthToken = redactSecret(redacted.AuthToken)

	fmt.Fprintf(state, fmt.FormatString(state, verb), redacted)
}
