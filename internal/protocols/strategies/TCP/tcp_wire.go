package TCP

import (
	"context"
	"fmt"
	"strings"

	pluginapi "github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
	log "github.com/sirupsen/logrus"
)

// runWirePlugins invokes the configured plugins in YAML declaration order.
// Unknown names are ignored here because the configuration validator reports
// them before startup. A faulty external plugin is isolated to its own hook so
// the remaining plugins and the TCP connection can continue.
func runWirePlugins(ctx context.Context, enabled []string, exchange *pluginapi.WireContext) {
	for _, configuredName := range enabled {
		name := strings.TrimSpace(configuredName)
		wirePlugin, ok := pluginapi.GetWire(name)
		if !ok {
			continue
		}
		invokeWirePlugin(ctx, name, wirePlugin, exchange)
	}
}

func invokeWirePlugin(ctx context.Context, name string, wirePlugin pluginapi.WirePlugin, exchange *pluginapi.WireContext) (applied bool) {
	candidate := cloneWireContext(exchange)
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Errorf("wire plugin %q panicked: %v", name, recovered)
			applied = false
		}
	}()
	if err := wirePlugin.OnExchange(ctx, candidate); err != nil {
		log.Errorf("wire plugin %q exchange failed: %v", name, err)
		return false
	}

	// Only the fields declared mutable by the public contract are committed.
	// Copies prevent a plugin from retaining aliases and mutating the exchange
	// asynchronously after its hook returns.
	exchange.Response = append([]byte(nil), candidate.Response...)
	exchange.CommandOutput = candidate.CommandOutput
	exchange.Metadata = cloneStringMap(candidate.Metadata)
	return true
}

func cloneWireContext(exchange *pluginapi.WireContext) *pluginapi.WireContext {
	clone := *exchange
	clone.Request = append([]byte(nil), exchange.Request...)
	clone.Response = append([]byte(nil), exchange.Response...)
	clone.Metadata = cloneStringMap(exchange.Metadata)
	clone.History = append([]pluginapi.Message(nil), exchange.History...)
	clone.Command.Patches = append([]pluginapi.WirePatch(nil), exchange.Command.Patches...)
	return &clone
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

// closeWireSessions notifies enabled plugins that retain per-connection state.
func closeWireSessions(ctx context.Context, connID string, enabled []string) {
	for _, configuredName := range enabled {
		name := strings.TrimSpace(configuredName)
		wirePlugin, ok := pluginapi.GetWire(name)
		if !ok {
			continue
		}
		closer, ok := wirePlugin.(pluginapi.WireSessionCloser)
		if !ok {
			continue
		}
		invokeWireSessionClose(ctx, name, closer, connID)
	}
}

func invokeWireSessionClose(ctx context.Context, name string, closer pluginapi.WireSessionCloser, connID string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Errorf("wire plugin %q session close panicked: %v", name, recovered)
		}
	}()
	if err := closer.OnSessionClose(ctx, connID); err != nil {
		log.Error(fmt.Errorf("wire plugin %q session close failed: %w", name, err))
	}
}
