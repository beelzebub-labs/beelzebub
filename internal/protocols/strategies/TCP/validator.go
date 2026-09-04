package TCP

import (
	"fmt"
	"strings"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	pluginapi "github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
)

type TCPValidator struct{}

func (v *TCPValidator) Name() string {
	return "tcp"
}

func (v *TCPValidator) Validate(config parser.BeelzebubServiceConfiguration) []parser.ValidationIssue {
	if config.Protocol != "tcp" {
		return nil
	}

	issues := parser.ValidateTLSConfig(config.TLSCertPath, config.TLSKeyPath, config.Filename)

	switch config.WireEncoding {
	case "", "utf8", "latin1":
	default:
		issues = append(issues, parser.ValidationIssue{
			Level:   parser.LevelError,
			Message: fmt.Sprintf("wireEncoding %q is invalid, valid: utf8, latin1", config.WireEncoding),
		})
	}

	issues = append(issues, validateFraming("framing", config.Framing)...)
	if config.WireEncoding == "latin1" && config.Framing == nil {
		issues = append(issues, parser.ValidationIssue{
			Level:   parser.LevelError,
			Message: "wireEncoding latin1 requires explicit framing so TCP fragments are not dispatched as complete binary messages",
		})
	}

	registered := pluginapi.WireNames()
	known := make(map[string]struct{}, len(registered))
	for _, name := range registered {
		known[name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(config.WirePlugins))
	for _, rawName := range config.WirePlugins {
		name := strings.TrimSpace(rawName)
		if name != rawName {
			issues = append(issues, parser.ValidationIssue{
				Level:   parser.LevelError,
				Message: fmt.Sprintf("wirePlugin %q must not contain surrounding whitespace", rawName),
			})
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			issues = append(issues, parser.ValidationIssue{Level: parser.LevelError, Message: fmt.Sprintf("wirePlugin %q is declared more than once", name)})
			continue
		}
		seen[name] = struct{}{}
		if _, ok := known[name]; !ok {
			issues = append(issues, parser.ValidationIssue{
				Level:   parser.LevelError,
				Message: fmt.Sprintf("wirePlugin %q is not registered (available: %s)", name, strings.Join(registered, ", ")),
			})
		}
	}

	for i, command := range config.Commands {
		issues = append(issues, validateFraming(fmt.Sprintf("command[%d].tlsFraming", i), command.TLSFraming)...)
		issues = append(issues, validateFraming(fmt.Sprintf("command[%d].nextFraming", i), command.NextFraming)...)
		if command.TLSFraming != nil && !command.TLSUpgrade {
			issues = append(issues, parser.ValidationIssue{
				Level:   parser.LevelError,
				Message: fmt.Sprintf("command[%d] declares tlsFraming without tlsUpgrade", i),
			})
		}
		if command.TLSUpgrade && command.CloseAfter {
			issues = append(issues, parser.ValidationIssue{
				Level:   parser.LevelWarning,
				Message: fmt.Sprintf("command[%d] enables tlsUpgrade and closeAfter; the TLS connection will close immediately after the handshake", i),
			})
		}
		if command.NextFraming != nil && command.TLSUpgrade {
			issues = append(issues, parser.ValidationIssue{
				Level:   parser.LevelError,
				Message: fmt.Sprintf("command[%d] cannot combine nextFraming with tlsUpgrade; use tlsFraming for the encrypted phase", i),
			})
		}
		for j, patch := range command.Patches {
			if strings.TrimSpace(patch.Type) == "" {
				issues = append(issues, parser.ValidationIssue{Level: parser.LevelError, Message: fmt.Sprintf("command[%d].patches[%d] has empty type", i, j)})
			}
			if patch.Offset < 0 || patch.Length < 0 {
				issues = append(issues, parser.ValidationIssue{Level: parser.LevelError, Message: fmt.Sprintf("command[%d].patches[%d] has a negative offset or length", i, j)})
			}
		}
	}

	return issues
}

func validateFraming(field string, framing *parser.Framing) []parser.ValidationIssue {
	if framing == nil {
		return nil
	}
	switch framing.Mode {
	case "ber":
		return nil
	case "fixed":
		if framing.FixedSize > 0 && framing.FixedSize <= maxFrameSize {
			return nil
		}
		return []parser.ValidationIssue{{Level: parser.LevelError, Message: field + " fixedSize is invalid"}}
	case "varint-length-prefix":
		if framing.LengthOffset >= 0 && framing.LengthOffset <= maxFrameSize &&
			framing.MaxLengthBytes >= 1 && framing.MaxLengthBytes <= 8 &&
			framing.LengthOffset <= maxFrameSize-framing.MaxLengthBytes {
			return nil
		}
		return []parser.ValidationIssue{{Level: parser.LevelError, Message: field + " varint-length-prefix fields are invalid"}}
	case "", "length-prefix":
		if framing.LengthOffset >= 0 && framing.LengthOffset <= maxFrameSize &&
			framing.LengthSize >= 1 && framing.LengthSize <= 8 &&
			framing.HeaderSize >= 0 && framing.HeaderSize <= maxFrameSize &&
			framing.LengthOffset <= maxFrameSize-framing.LengthSize {
			return nil
		}
		return []parser.ValidationIssue{{Level: parser.LevelError, Message: field + " length-prefix fields are invalid"}}
	default:
		return []parser.ValidationIssue{{Level: parser.LevelError, Message: fmt.Sprintf("%s mode %q is invalid", field, framing.Mode)}}
	}
}

func init() {
	parser.RegisterServiceValidator(&TCPValidator{})
}
