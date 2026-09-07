package plugins

import (
	"fmt"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	pluginapi "github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
)

type registryPluginValidator struct{}

func (registryPluginValidator) Name() string { return "plugin-registry" }

func (registryPluginValidator) Validate(config parser.BeelzebubServiceConfiguration) []parser.ValidationIssue {
	var issues []parser.ValidationIssue
	for i, command := range config.Commands {
		if command.Plugin == "" || supportsCommand(config.Protocol, command.Plugin) {
			continue
		}
		issues = append(issues, parser.ValidationIssue{
			Level:   parser.LevelError,
			Message: fmt.Sprintf("command[%d] references unknown or incompatible plugin %q", i, command.Plugin),
		})
	}
	if pluginName := config.FallbackCommand.Plugin; pluginName != "" && !supportsCommand(config.Protocol, pluginName) {
		issues = append(issues, parser.ValidationIssue{
			Level:   parser.LevelError,
			Message: fmt.Sprintf("fallbackCommand references unknown or incompatible plugin %q", pluginName),
		})
	}
	return issues
}

func supportsCommand(protocol, name string) bool {
	if _, ok := pluginapi.GetCommand(name); ok {
		return true
	}
	if protocol == "http" {
		_, ok := pluginapi.GetHTTP(name)
		return ok
	}
	return false
}

func init() {
	parser.RegisterServiceValidator(registryPluginValidator{})
}
