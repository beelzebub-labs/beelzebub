package TCP

import (
	"sync"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
)

// WireContext carries the state of a single TCP request/response exchange
// to wire-plugins. Wire-plugins observe or mutate the exchange after the
// public patch engine has run and before the response is written.
//
// All fields are non-nil unless noted; pointer fields are mutable.
type WireContext struct {
	// SessionKey groups exchanges by source for cross-connection state such as
	// LLM conversation history. It is derived from the peer address, so two
	// simultaneous connections from the same source share it — do NOT use it to
	// key per-connection handshake state (use ConnID for that).
	SessionKey string

	// ConnID uniquely identifies a single TCP connection (a fresh UUID per
	// accept). Wire-plugins that correlate state across exchanges within one
	// handshake (e.g. challenge/response stores) must key on ConnID so
	// concurrent connections from the same source IP don't clobber each other.
	ConnID string

	// Command is the matched command configuration for this exchange.
	Command *parser.Command

	// Request is the raw inbound bytes (Latin-1 decoded into []byte).
	Request []byte

	// Response is the outbound bytes prepared by the static handler / plugin
	// and the public patch engine. Wire-plugins may modify or replace it.
	Response *[]byte

	// Event is the trace event that will be emitted after wire-plugins return.
	// Wire-plugins may enrich it, e.g. by adding entries to Event.Metadata.
	Event *tracer.Event

	// Histories is the LLM conversation history for the session, useful for
	// wire-plugins that generate responses via an LLM.
	Histories []plugins.Message

	// ServiceConfig is the full service configuration. Wire-plugins that
	// need ServerName, plugin config, or other service-level state read it here.
	ServiceConfig parser.BeelzebubServiceConfiguration
}

// WirePlugin is invoked on every TCP exchange that matched a command.
// Implementations are registered via RegisterWirePlugin (typically from init()).
type WirePlugin interface {
	OnExchange(ctx *WireContext)
}

// SessionAware is an optional interface a WirePlugin may implement to release
// per-connection state when a connection ends. The TCP strategy calls
// OnSessionClose with the connection's ConnID for every registered plugin that
// implements it when handleTCPConnection returns, so plugins that keep
// per-connection maps (e.g. challenge stores) don't leak entries for incomplete
// handshakes.
type SessionAware interface {
	OnSessionClose(connID string)
}

type registeredWirePlugin struct {
	name   string
	plugin WirePlugin
}

var (
	wirePluginsMu sync.RWMutex
	wirePlugins   []registeredWirePlugin
)

// RegisterWirePlugin registers p under name, to be invoked on each exchange.
// A service runs a plugin only if its config's `wirePlugins` list contains the
// name (or the list is empty — then all registered plugins run). Order matters:
// plugins run in registration order.
func RegisterWirePlugin(name string, p WirePlugin) {
	wirePluginsMu.Lock()
	defer wirePluginsMu.Unlock()
	wirePlugins = append(wirePlugins, registeredWirePlugin{name: name, plugin: p})
}

func wirePluginSnapshot() []registeredWirePlugin {
	wirePluginsMu.RLock()
	defer wirePluginsMu.RUnlock()
	out := make([]registeredWirePlugin, len(wirePlugins))
	copy(out, wirePlugins)
	return out
}

// wirePluginEnabled reports whether a plugin named `name` should run for a
// service whose config declares `enabled`. An empty list means "run all",
// preserving behavior for configs that don't opt into per-service selection.
func wirePluginEnabled(name string, enabled []string) bool {
	if len(enabled) == 0 {
		return true
	}
	for _, e := range enabled {
		if e == name {
			return true
		}
	}
	return false
}

// runWirePlugins invokes every enabled wire-plugin in registration order.
// Safe to call with an empty registry (no-op).
func runWirePlugins(ctx *WireContext) {
	for _, rp := range wirePluginSnapshot() {
		if wirePluginEnabled(rp.name, ctx.ServiceConfig.WirePlugins) {
			rp.plugin.OnExchange(ctx)
		}
	}
}

// closeWireSessions notifies every enabled SessionAware wire-plugin that the
// connection identified by connID has ended, so they can release per-connection
// state. Safe to call with an empty registry (no-op).
func closeWireSessions(connID string, enabled []string) {
	for _, rp := range wirePluginSnapshot() {
		if !wirePluginEnabled(rp.name, enabled) {
			continue
		}
		if sa, ok := rp.plugin.(SessionAware); ok {
			sa.OnSessionClose(connID)
		}
	}
}
