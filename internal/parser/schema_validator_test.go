package parser

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSchemaValidator_Name(t *testing.T) {
	v := &SchemaValidator{}
	assert.Equal(t, "schema", v.Name())
}

func TestSchemaValidator_Validate(t *testing.T) {
	ResetSchemaCache()
	defer ResetSchemaCache()

	v := &SchemaValidator{}
	config := BeelzebubServiceConfiguration{
		Protocol:      "ssh",
		Address:       ":22",
		ServerVersion: "OpenSSH",
		PasswordRegex: "^(.+)$",
		ServerName:    "test",
		ApiVersion:    "v1",
		Commands:      []Command{{RegexStr: "^ls$", Handler: "files"}},
		Description:   "test",
	}
	issues := v.Validate(config)
	assert.Empty(t, issues)
}

func TestValidateConfigSchema_Valid(t *testing.T) {
	ResetSchemaCache()
	defer ResetSchemaCache()

	tests := []struct {
		name    string
		config  BeelzebubServiceConfiguration
		wantErr bool
	}{
		{
			name: "valid SSH",
			config: BeelzebubServiceConfiguration{
				Protocol: "ssh", Address: ":22",
				ServerVersion: "OpenSSH", PasswordRegex: "^(.+)$",
				Commands: []Command{{RegexStr: "^ls$", Handler: "files"}},
			},
		},
		{
			name: "valid HTTP",
			config: BeelzebubServiceConfiguration{
				Protocol: "http", Address: ":8080",
				Commands: []Command{{RegexStr: ".*", Handler: "ok"}},
			},
		},
		{
			name: "valid TCP no commands",
			config: BeelzebubServiceConfiguration{
				Protocol: "tcp", Address: ":3306",
				Banner: "8.0",
			},
		},
		{
			name: "valid MCP",
			config: BeelzebubServiceConfiguration{
				Protocol: "mcp", Address: ":8000",
				Tools: []Tool{
					{Name: "tool:test", Params: []Param{{Name: "arg", Description: "an arg"}}},
				},
			},
		},
		{
			name: "valid TELNET",
			config: BeelzebubServiceConfiguration{
				Protocol: "telnet", Address: ":23",
				PasswordRegex: "^(.+)$",
				Commands:      []Command{{RegexStr: "^ls$", Handler: "files"}},
			},
		},
		{
			name: "SSH with LLM",
			config: BeelzebubServiceConfiguration{
				Protocol: "ssh", Address: ":2222",
				ServerVersion: "OpenSSH", PasswordRegex: "^(.+)$",
				Commands: []Command{{RegexStr: "^(.+)$", Plugin: "LLMHoneypot"}},
				Plugin:   Plugin{LLMProvider: "openai", LLMModel: "gpt-4", OpenAISecretKey: "sk-..."},
			},
		},
		{
			name: "HTTP with Maze",
			config: BeelzebubServiceConfiguration{
				Protocol: "http", Address: ":8888",
				Commands: []Command{{RegexStr: ".*", Plugin: "MazeHoneypot"}},
			},
		},
		{
			name: "with rate limit",
			config: BeelzebubServiceConfiguration{
				Protocol: "ssh", Address: ":22",
				ServerVersion: "OpenSSH", PasswordRegex: "^(.+)$",
				Commands: []Command{{RegexStr: "^ls$", Handler: "files"}},
				Plugin: Plugin{
					RateLimitEnabled:       true,
					RateLimitRequests:      10,
					RateLimitWindowSeconds: 60,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := ValidateConfigSchema(tt.config)
			if tt.wantErr {
				assert.NotEmpty(t, issues)
			} else {
				assert.Empty(t, issues)
			}
		})
	}
}

func TestValidateConfigSchema_Invalid(t *testing.T) {
	ResetSchemaCache()
	defer ResetSchemaCache()

	tests := []struct {
		name   string
		config BeelzebubServiceConfiguration
		msg    string
	}{
		{
			name:   "missing protocol",
			config: BeelzebubServiceConfiguration{Address: ":22"},
			msg:    "value must be one of",
		},
		{
			name:   "missing address",
			config: BeelzebubServiceConfiguration{Protocol: "ssh"},
			msg:    "missing propert",
		},
		{
			name: "SSH missing passwordRegex",
			config: BeelzebubServiceConfiguration{
				Protocol: "ssh", Address: ":22",
				ServerVersion: "OpenSSH",
				Commands:      []Command{{RegexStr: "^ls$", Handler: "files"}},
			},
			msg: "missing property",
		},
		{
			name: "SSH missing serverVersion",
			config: BeelzebubServiceConfiguration{
				Protocol: "ssh", Address: ":22",
				PasswordRegex: "^(.+)$",
				Commands:      []Command{{RegexStr: "^ls$", Handler: "files"}},
			},
			msg: "missing property",
		},
		{
			name: "LLM without plugin object",
			config: BeelzebubServiceConfiguration{
				Protocol: "http", Address: ":80",
				Commands: []Command{{RegexStr: ".*", Plugin: "LLMHoneypot"}},
			},
			msg: "missing propert",
		},
		{
			name: "LLM without llmProvider",
			config: BeelzebubServiceConfiguration{
				Protocol: "http", Address: ":80",
				Commands: []Command{{RegexStr: ".*", Plugin: "LLMHoneypot"}},
				Plugin:   Plugin{LLMModel: "gpt-4"},
			},
			msg: "missing propert",
		},
		{
			name: "Maze on wrong protocol",
			config: BeelzebubServiceConfiguration{
				Protocol: "tcp", Address: ":8888",
				Commands: []Command{{RegexStr: ".*", Plugin: "MazeHoneypot"}},
			},
			msg: "value must be 'http'",
		},
		{
			name: "MCP with commands instead of tools",
			config: BeelzebubServiceConfiguration{
				Protocol: "mcp", Address: ":8000",
				Commands: []Command{{RegexStr: ".*", Handler: "ok"}},
			},
			msg: "false",
		},
		{
			name: "TELNET missing passwordRegex",
			config: BeelzebubServiceConfiguration{
				Protocol: "telnet", Address: ":23",
				Commands: []Command{{RegexStr: "^ls$", Handler: "files"}},
			},
			msg: "missing property",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := ValidateConfigSchema(tt.config)
			if !assert.NotEmpty(t, issues) {
				return
			}
			found := false
			for _, iss := range issues {
				if iss.Level == LevelError && strings.Contains(iss.Message, tt.msg) {
					found = true
					break
				}
			}
			assert.True(t, found, "expected error containing %q, got: %v", tt.msg, issues)
		})
	}
}

func TestValidateConfigSchema_UnknownProtocol(t *testing.T) {
	ResetSchemaCache()
	defer ResetSchemaCache()

	config := BeelzebubServiceConfiguration{
		Protocol: "ftp", Address: ":21",
	}
	issues := ValidateConfigSchema(config)
	assert.NotEmpty(t, issues)
	assert.Equal(t, LevelError, issues[0].Level)
	assert.Contains(t, issues[0].Message, "value must be one of")
}

func TestFlattenSchemaErrors_NonValidationError(t *testing.T) {
	issues := flattenSchemaErrors(errors.New("test error"))
	assert.Len(t, issues, 1)
	assert.Equal(t, LevelError, issues[0].Level)
	assert.Contains(t, issues[0].Message, "schema:")
}
