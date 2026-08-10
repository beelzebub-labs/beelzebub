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
