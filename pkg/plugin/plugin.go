// Package plugin defines the public SDK for beelzebub plugins.
//
// External plugins implement one of the interfaces below and register
// themselves via Register() inside their package init() function.
// The main binary then loads them via a blank import:
//
//	import _ "github.com/someone/beelzebub-myplugin"
package plugin

import (
	"context"
	"net/http"
)

// Metadata describes a registered plugin.
type Metadata struct {
	Name        string
	Description string
	Version     string
	Author      string
}

// Plugin is the base interface every plugin must satisfy.
type Plugin interface {
	Metadata() Metadata
}

// CommandPlugin generates text responses for command-oriented protocols
// (SSH, TCP, TELNET, HTTP body). It is the primary extension point for
// response-generation plugins such as LLM integrations.
type CommandPlugin interface {
	Plugin
	Execute(ctx context.Context, req CommandRequest) (string, error)
}

// HTTPPlugin generates full HTTP responses (status code, headers, body).
// Use this for plugins that need fine-grained control over the HTTP layer,
// such as directory-listing generators or custom web honeypots.
type HTTPPlugin interface {
	Plugin
	HandleHTTP(r *http.Request) HTTPResponse
}

// ServicePlugin is a long-running background service started alongside the
// honeypot
type ServicePlugin interface {
	Plugin
	Start(ctx context.Context) error
	Stop()
}

// WirePlugin observes and may rewrite one matched TCP request/response
// exchange. Wire plugins are compiled into Beelzebub and registered through
// Register, exactly like CommandPlugin and HTTPPlugin implementations.
//
// Implementations may be called concurrently for different connections and
// must synchronize their own shared state. Request and the service/command
// snapshots are read-only. Response, CommandOutput and Metadata may be replaced
// or enriched; those mutations are committed only when OnExchange returns nil.
// An error or panic discards that hook's mutations and does not prevent the next
// configured wire plugin from running.
type WirePlugin interface {
	Plugin
	OnExchange(ctx context.Context, exchange *WireContext) error
}

// WireSessionCloser is an optional lifecycle hook for WirePlugin
// implementations that retain per-connection state. The TCP strategy invokes
// it when the connection identified by connID ends. Implementations should
// honor ctx and return promptly so connection shutdown cannot be delayed.
type WireSessionCloser interface {
	OnSessionClose(ctx context.Context, connID string) error
}

// CommandRequest carries everything a CommandPlugin needs per invocation.
type CommandRequest struct {
	// Command is the raw input received from the attacker.
	Command string
	// ClientIP is the remote IP address of the attacker.
	ClientIP string
	// Protocol is the honeypot protocol ("http", "ssh", "tcp", "telnet").
	Protocol string
	// History is the conversation so far (for stateful/LLM plugins).
	History []Message
	// Config holds plugin-specific settings from the service YAML.
	Config Config
}

// Message is one turn in a multi-turn conversation.
type Message struct {
	Role    string
	Content string
}

// WireContext is the public, protocol-neutral view of one matched TCP
// exchange. It deliberately contains no internal parser or tracer types so an
// external plugin module can implement WirePlugin through the normal plugin
// installation flow.
type WireContext struct {
	// SessionKey groups exchanges by source for conversational history. It is
	// not unique per connection; use ConnID for handshake state.
	SessionKey string
	// ConnID uniquely identifies the accepted TCP connection.
	ConnID string
	// ClientIP and ClientPort identify the remote peer.
	ClientIP   string
	ClientPort string
	// Command describes the matched service command.
	Command WireCommand
	// Request is the exact inbound byte sequence and must be treated as read-only.
	Request []byte
	// Response is the outbound byte sequence and may be replaced by the plugin.
	Response []byte
	// CommandOutput is the logical output recorded in telemetry and history.
	CommandOutput string
	// Metadata is copied to the emitted interaction event.
	Metadata map[string]string
	// History contains the prior conversation turns for this source/service.
	History []Message
	// Service contains the public service fields relevant to wire processing.
	Service WireService
}

// WireCommand is the public snapshot of the command matched by the TCP
// strategy.
type WireCommand struct {
	Name         string
	Handler      string
	Plugin       string
	CloseAfter   bool
	TLSUpgrade   bool
	BinaryOutput bool
	Patches      []WirePatch
}

// WirePatch describes one configured binary response patch. The core handles
// generic patch types; WirePlugin implementations may define additional types.
type WirePatch struct {
	Type   string
	Offset int
	Length int
}

// WireService is the stable public subset of a TCP service configuration made
// available to WirePlugin implementations.
type WireService struct {
	Protocol      string
	Address       string
	Description   string
	ServerName    string
	ServerVersion string
	WireEncoding  string
	Config        Config
}

// HTTPResponse carries a full HTTP response from an HTTPPlugin.
type HTTPResponse struct {
	StatusCode  int
	Body        string
	Headers     map[string]string
	ContentType string
}

// Config carries plugin-specific settings extracted from the service YAML.
// All fields are optional; plugins use only what they need.
type Config struct {
	LLMProvider             string
	LLMModel                string
	OpenAISecretKey         string
	Host                    string
	Prompt                  string
	InputValidationEnabled  bool
	InputValidationPrompt   string
	OutputValidationEnabled bool
	OutputValidationPrompt  string
	RateLimitEnabled        bool
	RateLimitRequests       int
	RateLimitWindowSeconds  int
	ServerVersion           string
	ServerName              string
}
