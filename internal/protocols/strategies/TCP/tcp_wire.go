package TCP

import (
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
	// SessionKey uniquely identifies the connection for cross-exchange
	// correlation (e.g. a value stored on one exchange and consumed on a
	// later exchange within the same session).
	SessionKey string

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

var wirePlugins []WirePlugin

// RegisterWirePlugin appends p to the list of wire-plugins invoked on each
// exchange. Order matters: plugins run in registration order.
func RegisterWirePlugin(p WirePlugin) {
	wirePlugins = append(wirePlugins, p)
}

// runWirePlugins invokes every registered wire-plugin in registration order.
// Safe to call with an empty registry (no-op).
func runWirePlugins(ctx *WireContext) {
	for _, p := range wirePlugins {
		p.OnExchange(ctx)
	}
}
