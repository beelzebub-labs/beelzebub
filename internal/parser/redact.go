package parser

import (
	"fmt"
	"strings"
)

// redactedPlaceholder replaces secret values when a configuration struct is
// formatted for logging.
const redactedPlaceholder = "[REDACTED]"

// redactSecret hides a secret value while preserving the distinction between
// "configured" and "not configured", which debug logs still need to be useful.
func redactSecret(value string) string {
	if value == "" {
		return ""
	}
	return redactedPlaceholder
}

// Format redacts the OpenAI secret key whenever a Plugin is printed.
//
// Debug logging dumps the whole service configuration (see readConfigurationsServices),
// so redaction belongs on the struct rather than at each call site: any future
// log line that formats a configuration is then safe by construction.
//
// Implementing fmt.Formatter rather than fmt.Stringer preserves the caller's
// verb and flags, so "%+v" keeps printing field names.
//
// This deliberately does not touch MarshalJSON: HashCode marshals the
// configuration to detect drift, and redacting there would make the hash of
// every secret-bearing configuration identical.
func (plugin Plugin) Format(state fmt.State, verb rune) {
	type plain Plugin

	redacted := plain(plugin)
	redacted.OpenAISecretKey = redactSecret(redacted.OpenAISecretKey)

	fmt.Fprintf(state, fmt.FormatString(state, verb), redacted)
}

// sensitiveConfigKeys are the configuration keys whose values are secrets.
//
// Matching is exact rather than by substring on purpose: "passwordRegex" is not
// a secret, and redacting it would make debug logs useless for the SSH
// honeypot. A new secret-bearing configuration field must be added here.
var sensitiveConfigKeys = map[string]struct{}{
	"openaisecretkey": {},
	"auth-token":      {},
	"authtoken":       {},
}

// Format redacts secrets whenever a service configuration is printed.
//
// The typed Plugin field is handled by Plugin.Format, but RawConfig holds the
// original parsed document and would otherwise reprint every secret verbatim.
func (config BeelzebubServiceConfiguration) Format(state fmt.State, verb rune) {
	type plain BeelzebubServiceConfiguration

	redacted := plain(config)
	redacted.RawConfig = redactRawConfig(config.RawConfig)

	fmt.Fprintf(state, fmt.FormatString(state, verb), redacted)
}

// redactRawConfig returns a copy of a parsed configuration document with secret
// values replaced. It copies rather than edits in place: the caller is only
// formatting the configuration, and must not damage the one being served.
func redactRawConfig(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, nested := range typed {
			if _, sensitive := sensitiveConfigKeys[strings.ToLower(key)]; sensitive {
				if text, ok := nested.(string); ok {
					result[key] = redactSecret(text)
					continue
				}
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
