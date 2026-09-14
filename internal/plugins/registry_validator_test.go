package plugins

import (
	"context"
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	pluginapi "github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
)

type registryValidatorTestPlugin struct{}

func (registryValidatorTestPlugin) Metadata() pluginapi.Metadata {
	return pluginapi.Metadata{Name: "registry-validator-test"}
}

func (registryValidatorTestPlugin) Execute(context.Context, pluginapi.CommandRequest) (string, error) {
	return "ok", nil
}

func TestRegistryPluginValidatorAcceptsRegisteredExternalCommandPlugin(t *testing.T) {
	if _, ok := pluginapi.Get("registry-validator-test"); !ok {
		pluginapi.Register(registryValidatorTestPlugin{})
	}
	issues := registryPluginValidator{}.Validate(parser.BeelzebubServiceConfiguration{
		Protocol: "tcp",
		Commands: []parser.Command{{Plugin: "registry-validator-test"}},
	})
	if len(issues) != 0 {
		t.Fatalf("registered external plugin was rejected: %v", issues)
	}
}

func TestRegistryPluginValidatorRejectsUnknownPlugin(t *testing.T) {
	issues := registryPluginValidator{}.Validate(parser.BeelzebubServiceConfiguration{
		Protocol: "tcp",
		Commands: []parser.Command{{Plugin: "definitely-not-registered"}},
	})
	if len(issues) != 1 || issues[0].Level != parser.LevelError {
		t.Fatalf("unknown plugin issues = %v", issues)
	}
}
