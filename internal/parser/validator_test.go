package parser

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMain(m *testing.M) {
	ResetServiceValidators()
	os.Exit(m.Run())
}

func makeService(filename, protocol, address string, commands []Command) BeelzebubServiceConfiguration {
	return BeelzebubServiceConfiguration{
		Filename: filename,
		Protocol: protocol,
		Address:  address,
		Commands: commands,
	}
}

func findIssues(result ValidateResult, filename string) []ValidationIssue {
	for _, r := range result.Results {
		if r.Filename == filename {
			return r.Issues
		}
	}
	return nil
}

func hasIssue(issues []ValidationIssue, level, message string) bool {
	for _, issue := range issues {
		if issue.Level == level && issue.Message == message {
			return true
		}
	}
	return false
}

func hasIssueContaining(issues []ValidationIssue, level, substr string) bool {
	for _, issue := range issues {
		if issue.Level == level && strings.Contains(issue.Message, substr) {
			return true
		}
	}
	return false
}

func TestValidateProtocol(t *testing.T) {
	ResetSchemaCache()
	defer ResetSchemaCache()

	tests := []struct {
		name      string
		protocol  string
		wantError bool
		errorMsg  string
	}{
		{"missing protocol", "", true, "value must be one of"},
		{"invalid protocol ftp", "ftp", true, "value must be one of"},
		{"valid http", "http", false, ""},
		{"valid ssh", "ssh", false, ""},
		{"valid tcp", "tcp", false, ""},
		{"valid mcp", "mcp", false, ""},
		{"valid telnet", "telnet", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := makeService("test.yaml", tt.protocol, ":8080", nil)
			issues := ValidateConfigSchema(svc)

			if tt.wantError {
				if !hasIssueContaining(issues, LevelError, tt.errorMsg) {
					t.Logf("Issues (count=%d):", len(issues))
					for i, iss := range issues {
						t.Logf("  [%d] Level=%s Msg=%q", i, iss.Level, iss.Message)
					}
				}
				assert.True(t, hasIssueContaining(issues, LevelError, tt.errorMsg))
			} else {
				for _, issue := range issues {
					assert.NotContains(t, issue.Message, "value must be one of")
				}
			}
		})
	}
}

func TestValidateAddress(t *testing.T) {
	tests := []struct {
		name      string
		address   string
		wantError bool
	}{
		{"empty address", "", true},
		{"whitespace-only address", "   ", true},
		{"valid address", ":8080", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := makeService("test.yaml", "http", tt.address, nil)
			result := Validate([]BeelzebubServiceConfiguration{svc}, nil)
			issues := findIssues(result, "test.yaml")

			if tt.wantError {
				assert.True(t, hasIssue(issues, LevelError, "address is empty"))
			} else {
				assert.False(t, hasIssue(issues, LevelError, "address is empty"))
			}
		})
	}
}

func TestValidateCommandRegexEmpty(t *testing.T) {
	tests := []struct {
		name      string
		commands  []Command
		wantError bool
	}{
		{
			name:      "empty regex",
			commands:  []Command{{RegexStr: ""}},
			wantError: true,
		},
		{
			name:      "non-empty regex",
			commands:  []Command{{RegexStr: "wp-admin"}},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := makeService("test.yaml", "http", ":8080", tt.commands)
			result := Validate([]BeelzebubServiceConfiguration{svc}, nil)
			issues := findIssues(result, "test.yaml")

			if tt.wantError {
				assert.True(t, hasIssue(issues, LevelError, "command[0] has empty regex"))
			} else {
				assert.False(t, hasIssue(issues, LevelError, "command[0] has empty regex"))
			}
		})
	}
}

func TestValidateCommandPluginInvalid(t *testing.T) {
	RegisterServiceValidator(&SchemaValidator{})
	defer ResetServiceValidators()

	tests := []struct {
		name      string
		plugin    string
		wantError bool
	}{
		{"typo plugin name", "TypoPlugin", true},
		{"empty plugin", "", false},
		{"LLMHoneypot", "LLMHoneypot", false},
		{"MazeHoneypot", "MazeHoneypot", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := makeService("test.yaml", "http", ":8080", []Command{
				{RegexStr: "test", Plugin: tt.plugin},
			})
			result := Validate([]BeelzebubServiceConfiguration{svc}, nil)
			issues := findIssues(result, "test.yaml")

			if tt.wantError {
				assert.True(t, hasIssueContaining(issues, LevelError, "value must be one of"),
					"expected schema validation error for plugin %q, got: %v", tt.plugin, issues)
			} else {
				assert.False(t, hasIssueContaining(issues, LevelError, "value must be one of"),
					"unexpected schema validation error for plugin %q", tt.plugin)
			}
		})
	}
}

func TestValidateFallbackCommand(t *testing.T) {
	RegisterServiceValidator(&SchemaValidator{})
	defer ResetServiceValidators()

	tests := []struct {
		name          string
		fallback      Command
		wantRegexErr  bool
		wantPluginErr bool
	}{
		{
			name:          "empty handler and plugin",
			fallback:      Command{},
			wantRegexErr:  false,
			wantPluginErr: false,
		},
		{
			name:          "invalid plugin with valid regex",
			fallback:      Command{Handler: "test", Plugin: "BadPlugin", RegexStr: ".*"},
			wantRegexErr:  false,
			wantPluginErr: true,
		},
		{
			name:          "empty regex with valid plugin (fallback is catch-all, no regex needed)",
			fallback:      Command{Handler: "test", Plugin: "LLMHoneypot"},
			wantRegexErr:  false,
			wantPluginErr: false,
		},
		{
			name:          "invalid regex syntax in fallback",
			fallback:      Command{Handler: "test", Plugin: "LLMHoneypot", RegexStr: "[invalid"},
			wantRegexErr:  true,
			wantPluginErr: false,
		},
		{
			name:          "valid regex and valid plugin",
			fallback:      Command{Handler: "test", Plugin: "MazeHoneypot", RegexStr: ".*"},
			wantRegexErr:  false,
			wantPluginErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := makeService("test.yaml", "http", ":8080", nil)
			svc.FallbackCommand = tt.fallback
			result := Validate([]BeelzebubServiceConfiguration{svc}, nil)
			issues := findIssues(result, "test.yaml")

			if tt.wantRegexErr {
				assert.True(t, hasIssueContaining(issues, LevelError, "fallbackCommand has invalid regex"),
					"expected regex error, got: %v", issues)
			}

			if tt.wantPluginErr {
				assert.True(t, hasIssueContaining(issues, LevelError, "value must be one of"),
					"expected schema plugin error, got: %v", issues)
			}
		})
	}
}

func TestValidatePortCollision(t *testing.T) {
	tests := []struct {
		name      string
		services  []BeelzebubServiceConfiguration
		wantError bool
	}{
		{
			name: "same protocol and address",
			services: []BeelzebubServiceConfiguration{
				makeService("a.yaml", "http", ":8080", nil),
				makeService("b.yaml", "http", ":8080", nil),
			},
			wantError: true,
		},
		{
			name: "different addresses",
			services: []BeelzebubServiceConfiguration{
				makeService("a.yaml", "http", ":8080", nil),
				makeService("b.yaml", "http", ":9090", nil),
			},
			wantError: false,
		},
		{
			name: "same address different protocol",
			services: []BeelzebubServiceConfiguration{
				makeService("a.yaml", "http", ":8080", nil),
				makeService("b.yaml", "ssh", ":8080", nil),
			},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Validate(tt.services, nil)

			for _, svc := range tt.services {
				issues := findIssues(result, svc.Filename)
				expectedMsg := fmt.Sprintf("address %s:%s is used by multiple services", svc.Protocol, svc.Address)
				if tt.wantError {
					assert.True(t, hasIssue(issues, LevelError, expectedMsg))
				} else {
					assert.False(t, hasIssue(issues, LevelError, expectedMsg))
				}
			}
		})
	}
}

func TestValidateInlineSecretKey(t *testing.T) {
	svc := makeService("test.yaml", "http", ":8080", []Command{
		{RegexStr: "test", Plugin: "LLMHoneypot"},
	})
	svc.Plugin.OpenAISecretKey = "sk-12345"
	result := Validate([]BeelzebubServiceConfiguration{svc}, nil)
	issues := findIssues(result, "test.yaml")
	assert.True(t, hasIssue(issues, LevelWarning, "openAISecretKey is set inline in config — prefer using the OPEN_AI_SECRET_KEY environment variable to avoid exposing secrets in version control"))
}

func TestValidateDeadlineTimeout(t *testing.T) {
	tests := []struct {
		name     string
		timeout  int
		commands []Command
		wantWarn bool
	}{
		{"tcp zero with commands", 0, []Command{{RegexStr: "test"}}, true},
		{"zero without commands", 0, nil, false},
		{"non-zero with commands", 30, []Command{{RegexStr: "test"}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := makeService("test.yaml", "tcp", ":8080", tt.commands)
			svc.DeadlineTimeoutSeconds = tt.timeout
			result := Validate([]BeelzebubServiceConfiguration{svc}, nil)
			issues := findIssues(result, "test.yaml")

			if tt.wantWarn {
				assert.True(t, hasIssue(issues, LevelWarning, "deadlineTimeoutSeconds is not set, connections may be closed immediately"))
			} else {
				assert.False(t, hasIssue(issues, LevelWarning, "deadlineTimeoutSeconds is not set"))
			}
		})
	}
}

func TestValidateDeadlineTimeoutHTTPDoesNotWarn(t *testing.T) {
	svc := makeService("test.yaml", "http", ":8080", []Command{{RegexStr: "test"}})
	result := Validate([]BeelzebubServiceConfiguration{svc}, nil)
	assert.False(t, hasIssueContaining(findIssues(result, "test.yaml"), LevelWarning, "deadlineTimeoutSeconds"))
}

func TestValidateIntentionalHTTPEmptyResponses(t *testing.T) {
	tests := []Command{
		{RegexStr: "a", StatusCode: 204},
		{RegexStr: "b", StatusCode: 205},
		{RegexStr: "c", StatusCode: 304},
		{RegexStr: "d", StatusCode: 302, Headers: []string{"location: /login"}},
		{RegexStr: "e", StatusCode: 401, Headers: []string{"WWW-AUTHENTICATE: Basic realm=api"}},
	}
	result := Validate([]BeelzebubServiceConfiguration{makeService("test.yaml", "http", ":8080", tests)}, nil)
	assert.False(t, hasIssueContaining(findIssues(result, "test.yaml"), LevelWarning, "empty handler"))
}

func TestValidateHTTPEmptyHandlerStillWarnsWhenNotIntentional(t *testing.T) {
	svc := makeService("test.yaml", "http", ":8080", []Command{{RegexStr: "test", StatusCode: 200}})
	result := Validate([]BeelzebubServiceConfiguration{svc}, nil)
	assert.True(t, hasIssueContaining(findIssues(result, "test.yaml"), LevelWarning, "empty handler"))
}

func TestValidateWithParseIssues(t *testing.T) {
	parseIssues := []ValidationIssue{
		{Level: LevelError, Message: "YAML parse error: ...", Filename: "bad.yaml"},
		{Level: LevelWarning, Message: "deprecated field", Filename: "old.yaml"},
	}

	result := Validate(nil, parseIssues)

	assert.Len(t, result.Results, 2)
	assert.Equal(t, 1, result.TotalErrors)
	assert.Equal(t, 1, result.TotalWarnings)

	badIssues := findIssues(result, "bad.yaml")
	assert.True(t, hasIssue(badIssues, LevelError, "YAML parse error: ..."))

	oldIssues := findIssues(result, "old.yaml")
	assert.True(t, hasIssue(oldIssues, LevelWarning, "deprecated field"))
}

func TestValidatePrint(t *testing.T) {
	tests := []struct {
		name   string
		result ValidateResult
		want   string
	}{
		{
			name: "no issues",
			result: ValidateResult{
				Results: []ValidationResult{
					{Filename: "service.yaml"},
				},
			},
			want: "OK service.yaml\n\n0 errors, 0 warnings\n",
		},
		{
			name: "errors",
			result: ValidateResult{
				Results: []ValidationResult{
					{
						Filename: "service.yaml",
						Issues: []ValidationIssue{
							{Level: LevelError, Message: "address is empty"},
						},
					},
				},
				TotalErrors: 1,
			},
			want: "FAIL service.yaml\n  ERROR: address is empty\n\n1 errors, 0 warnings\n",
		},
		{
			name: "warnings only",
			result: ValidateResult{
				Results: []ValidationResult{
					{
						Filename: "service.yaml",
						Issues: []ValidationIssue{
							{Level: LevelWarning, Message: "some warning"},
						},
					},
				},
				TotalWarnings: 1,
			},
			want: "WARN service.yaml\n  WARNING: some warning\n\n0 errors, 1 warnings\n",
		},
		{
			name: "sorted by filename",
			result: ValidateResult{
				Results: []ValidationResult{
					{Filename: "z.yaml"},
					{Filename: "a.yaml"},
				},
			},
			want: "OK a.yaml\nOK z.yaml\n\n0 errors, 0 warnings\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			tt.result.Print()

			w.Close()
			os.Stdout = oldStdout

			var buf bytes.Buffer
			io.Copy(&buf, r)
			got := buf.String()

			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateExitCode(t *testing.T) {
	tests := []struct {
		name     string
		result   ValidateResult
		wantCode int
	}{
		{"no errors", ValidateResult{TotalErrors: 0}, 0},
		{"has errors", ValidateResult{TotalErrors: 3}, 1},
		{"only warnings", ValidateResult{TotalErrors: 0, TotalWarnings: 5}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantCode, tt.result.ExitCode())
		})
	}
}

func TestValidateCore(t *testing.T) {
	t.Run("all valid", func(t *testing.T) {
		config := &BeelzebubCoreConfigurations{}
		result := ValidateCore(config, "beelzebub.yaml")
		assert.Equal(t, 0, result.TotalErrors)
	})

	t.Run("custom filename", func(t *testing.T) {
		config := &BeelzebubCoreConfigurations{}
		config.Core.Tracings.RabbitMQ.Enabled = true
		result := ValidateCore(config, "/custom/path/core.yaml")
		assert.Equal(t, 1, result.TotalErrors)
		assert.Equal(t, "/custom/path/core.yaml", result.Results[0].Filename)
		assert.True(t, hasIssue(result.Results[0].Issues, LevelError, "rabbitMQ is enabled but URI is empty"))
	})

	t.Run("rabbitmq enabled with empty URI", func(t *testing.T) {
		config := &BeelzebubCoreConfigurations{}
		config.Core.Tracings.RabbitMQ.Enabled = true
		result := ValidateCore(config, "beelzebub.yaml")
		assert.Equal(t, 1, result.TotalErrors)
		assert.True(t, hasIssue(result.Results[0].Issues, LevelError, "rabbitMQ is enabled but URI is empty"))
	})

	t.Run("rabbitmq enabled with URI set", func(t *testing.T) {
		config := &BeelzebubCoreConfigurations{}
		config.Core.Tracings.RabbitMQ.Enabled = true
		config.Core.Tracings.RabbitMQ.URI = "amqp://localhost"
		result := ValidateCore(config, "beelzebub.yaml")
		assert.Equal(t, 0, result.TotalErrors)
	})

	t.Run("beelzebub-cloud enabled with empty URI and auth-token", func(t *testing.T) {
		config := &BeelzebubCoreConfigurations{}
		config.Core.BeelzebubCloud.Enabled = true
		result := ValidateCore(config, "beelzebub.yaml")
		assert.Equal(t, 2, result.TotalErrors)
	})

	t.Run("beelzebub-cloud enabled with URI but empty auth-token", func(t *testing.T) {
		config := &BeelzebubCoreConfigurations{}
		config.Core.BeelzebubCloud.Enabled = true
		config.Core.BeelzebubCloud.URI = "https://api.example.com"
		result := ValidateCore(config, "beelzebub.yaml")
		assert.Equal(t, 1, result.TotalErrors)
		assert.True(t, hasIssue(result.Results[0].Issues, LevelError, "beelzebub-cloud is enabled but auth-token is empty"))
	})

	t.Run("beelzebub-cloud fully configured", func(t *testing.T) {
		config := &BeelzebubCoreConfigurations{}
		config.Core.BeelzebubCloud.Enabled = true
		config.Core.BeelzebubCloud.URI = "https://api.example.com"
		config.Core.BeelzebubCloud.AuthToken = "token123"
		result := ValidateCore(config, "beelzebub.yaml")
		assert.Equal(t, 0, result.TotalErrors)
	})

	t.Run("multiple core issues", func(t *testing.T) {
		config := &BeelzebubCoreConfigurations{}
		config.Core.Tracings.RabbitMQ.Enabled = true
		config.Core.BeelzebubCloud.Enabled = true
		result := ValidateCore(config, "beelzebub.yaml")
		assert.Equal(t, 3, result.TotalErrors)
	})

	t.Run("prometheus all valid", func(t *testing.T) {
		config := &BeelzebubCoreConfigurations{}
		config.Core.Prometheus.Path = "/metrics"
		config.Core.Prometheus.Port = ":9090"
		result := ValidateCore(config, "beelzebub.yaml")
		assert.Equal(t, 0, result.TotalErrors)
		assert.Equal(t, 0, result.TotalWarnings)
	})

	t.Run("prometheus path empty", func(t *testing.T) {
		config := &BeelzebubCoreConfigurations{}
		config.Core.Prometheus.Port = ":9090"
		result := ValidateCore(config, "beelzebub.yaml")
		assert.Equal(t, 1, result.TotalErrors)
		assert.True(t, hasIssue(result.Results[0].Issues, LevelError, "prometheus is configured but path is empty"))
	})

	t.Run("prometheus path missing leading slash", func(t *testing.T) {
		config := &BeelzebubCoreConfigurations{}
		config.Core.Prometheus.Path = "metrics"
		config.Core.Prometheus.Port = ":9090"
		result := ValidateCore(config, "beelzebub.yaml")
		assert.Equal(t, 1, result.TotalErrors)
		assert.True(t, hasIssue(result.Results[0].Issues, LevelError, "prometheus path \"metrics\" must start with /"))
	})

	t.Run("prometheus port empty", func(t *testing.T) {
		config := &BeelzebubCoreConfigurations{}
		config.Core.Prometheus.Path = "/metrics"
		result := ValidateCore(config, "beelzebub.yaml")
		assert.Equal(t, 1, result.TotalErrors)
		assert.True(t, hasIssue(result.Results[0].Issues, LevelError, "prometheus is configured but port is empty"))
	})

	t.Run("prometheus both empty", func(t *testing.T) {
		config := &BeelzebubCoreConfigurations{}
		config.Core.Prometheus.Path = ""
		config.Core.Prometheus.Port = ""
		result := ValidateCore(config, "beelzebub.yaml")
		assert.Equal(t, 0, result.TotalErrors)
	})

	t.Run("prometheus path empty and port empty", func(t *testing.T) {
		config := &BeelzebubCoreConfigurations{}
		result := ValidateCore(config, "beelzebub.yaml")
		assert.Equal(t, 0, result.TotalErrors)
	})

	t.Run("logs path parent dir does not exist", func(t *testing.T) {
		config := &BeelzebubCoreConfigurations{}
		config.Core.Logging.LogsPath = "/nonexistent/dir/beelzebub.log"
		result := ValidateCore(config, "beelzebub.yaml")
		for _, issue := range result.Results[0].Issues {
			t.Logf("issue: %+v", issue)
		}
		assert.Equal(t, 1, result.TotalWarnings)
		assert.Equal(t, 0, result.TotalErrors)
		assert.True(t, hasIssue(result.Results[0].Issues, LevelWarning, `logs path "/nonexistent/dir/beelzebub.log": parent directory "/nonexistent/dir" does not exist`))
	})

	t.Run("logs path empty", func(t *testing.T) {
		config := &BeelzebubCoreConfigurations{}
		config.Core.Logging.LogsPath = ""
		result := ValidateCore(config, "beelzebub.yaml")
		assert.Equal(t, 0, result.TotalErrors)
		assert.Equal(t, 0, result.TotalWarnings)
	})

	t.Run("logs path valid parent dir", func(t *testing.T) {
		config := &BeelzebubCoreConfigurations{}
		config.Core.Logging.LogsPath = "/tmp/beelzebub.log"
		result := ValidateCore(config, "beelzebub.yaml")
		assert.Equal(t, 0, result.TotalErrors)
		assert.Equal(t, 0, result.TotalWarnings)
	})
}

func TestValidateAddressFormat(t *testing.T) {
	tests := []struct {
		name     string
		address  string
		wantWarn bool
		warnMsg  string
	}{
		{":8080", ":8080", false, ""},
		{"0.0.0.0:80", "0.0.0.0:80", false, ""},
		{"/tmp/socket", "/tmp/socket", false, ""},
		{"abc no colon", "abc", true, `address "abc" has invalid port format or port out of range (1-65535)`},
		{":99999 port out of range", ":99999", true, `address ":99999" has invalid port format or port out of range (1-65535)`},
		{":0 port zero", ":0", true, `address ":0" has invalid port format or port out of range (1-65535)`},
		{":abc port not a number", ":abc", true, `address ":abc" has invalid port format or port out of range (1-65535)`},
		{"[::1]:8080 IPv6", "[::1]:8080", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := makeService("test.yaml", "http", tt.address, nil)
			result := Validate([]BeelzebubServiceConfiguration{svc}, nil)
			issues := findIssues(result, "test.yaml")

			if tt.wantWarn {
				assert.True(t, hasIssue(issues, LevelWarning, tt.warnMsg))
			} else {
				for _, issue := range issues {
					assert.NotContains(t, issue.Message, "invalid port format")
				}
			}
		})
	}
}

func TestValidateCommandEmptyHandlerAndPlugin(t *testing.T) {
	tests := []struct {
		name     string
		commands []Command
		wantWarn bool
	}{
		{"handler set", []Command{{RegexStr: "test", Handler: "some-handler"}}, false},
		{"plugin set", []Command{{RegexStr: "test", Plugin: "LLMHoneypot"}}, false},
		{"both empty", []Command{{RegexStr: "test"}}, true},
		{"both set", []Command{{RegexStr: "test", Handler: "h", Plugin: "LLMHoneypot"}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := makeService("test.yaml", "http", ":8080", tt.commands)
			result := Validate([]BeelzebubServiceConfiguration{svc}, nil)
			issues := findIssues(result, "test.yaml")

			if tt.wantWarn {
				assert.True(t, hasIssue(issues, LevelWarning, "command[0] has empty handler and no plugin, matched requests will produce no output"))
			} else {
				assert.False(t, hasIssue(issues, LevelWarning, "command[0] has empty handler and no plugin"))
			}
		})
	}
}

func TestValidateMalformedHeaders(t *testing.T) {
	tests := []struct {
		name     string
		commands []Command
		wantWarn bool
	}{
		{"no headers", []Command{{RegexStr: "test", Headers: nil}}, false},
		{"valid header", []Command{{RegexStr: "test", Headers: []string{"Content-Type: application/json"}}}, false},
		{"malformed header", []Command{{RegexStr: "test", Headers: []string{"NoColonValue"}}}, true},
		{"multiple headers one malformed", []Command{{RegexStr: "test", Headers: []string{"Content-Type: application/json", "NoColonValue"}}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := makeService("test.yaml", "http", ":8080", tt.commands)
			result := Validate([]BeelzebubServiceConfiguration{svc}, nil)
			issues := findIssues(result, "test.yaml")

			if tt.wantWarn {
				assert.True(t, hasIssue(issues, LevelWarning, `command[0].headers has malformed entry (no colon): "NoColonValue"`))
			} else {
				for _, issue := range issues {
					assert.NotContains(t, issue.Message, "malformed entry")
				}
			}
		})
	}
}

type mockValidator struct {
	name   string
	issues []ValidationIssue
}

func (m *mockValidator) Name() string { return m.name }

func (m *mockValidator) Validate(config BeelzebubServiceConfiguration) []ValidationIssue {
	return m.issues
}

func TestRegisterAndResetServiceValidators(t *testing.T) {
	ResetServiceValidators()

	mockV := &mockValidator{name: "mock", issues: []ValidationIssue{
		{Level: LevelWarning, Message: "mock issue"},
	}}
	RegisterServiceValidator(mockV)

	svc := makeService("test.yaml", "tcp", ":8080", nil)
	result := Validate([]BeelzebubServiceConfiguration{svc}, nil)
	issues := findIssues(result, "test.yaml")
	assert.True(t, hasIssue(issues, LevelWarning, "mock issue"))

	ResetServiceValidators()

	result2 := Validate([]BeelzebubServiceConfiguration{svc}, nil)
	issues2 := findIssues(result2, "test.yaml")
	assert.False(t, hasIssue(issues2, LevelWarning, "mock issue"))
}

func TestValidateTLSConfig(t *testing.T) {
	t.Run("both empty", func(t *testing.T) {
		issues := ValidateTLSConfig("", "", "test.yaml")
		assert.Empty(t, issues)
	})

	t.Run("both set and exist", func(t *testing.T) {
		existingFile, _ := os.Executable()
		if existingFile == "" {
			existingFile = "/tmp"
		}
		issues := ValidateTLSConfig(existingFile, existingFile, "test.yaml")
		assert.Empty(t, issues)
	})

	t.Run("only cert set", func(t *testing.T) {
		issues := ValidateTLSConfig("/tmp/cert.crt", "", "test.yaml")
		assert.Len(t, issues, 1)
		assert.Equal(t, LevelError, issues[0].Level)
		assert.Equal(t, "both tlsCertPath and tlsKeyPath must be set for TLS, or neither", issues[0].Message)
		assert.Equal(t, "test.yaml", issues[0].Filename)
	})

	t.Run("only key set", func(t *testing.T) {
		issues := ValidateTLSConfig("", "/tmp/cert.key", "test.yaml")
		assert.Len(t, issues, 1)
		assert.Equal(t, LevelError, issues[0].Level)
	})

	t.Run("both set but files do not exist", func(t *testing.T) {
		issues := ValidateTLSConfig("/nonexistent/cert.crt", "/nonexistent/cert.key", "test.yaml")
		assert.Len(t, issues, 2)
		for _, issue := range issues {
			assert.Equal(t, LevelWarning, issue.Level)
			assert.Contains(t, issue.Message, "does not exist")
			assert.Equal(t, "test.yaml", issue.Filename)
		}
	})

	t.Run("one file does not exist", func(t *testing.T) {
		existingFile, _ := os.Executable()
		if existingFile == "" {
			existingFile = "/tmp"
		}
		issues := ValidateTLSConfig(existingFile, "/nonexistent/cert.key", "test.yaml")
		assert.Len(t, issues, 1)
		assert.Equal(t, LevelWarning, issues[0].Level)
		assert.Contains(t, issues[0].Message, "tlsKeyPath file does not exist")
	})
}

func TestValidatePasswordRegex(t *testing.T) {
	t.Run("empty regex", func(t *testing.T) {
		issues := ValidatePasswordRegex("", "ssh", "test.yaml")
		assert.Len(t, issues, 1)
		assert.Equal(t, LevelError, issues[0].Level)
		assert.Equal(t, "passwordRegex is required for ssh protocol", issues[0].Message)
		assert.Equal(t, "test.yaml", issues[0].Filename)
	})

	t.Run("invalid regex", func(t *testing.T) {
		issues := ValidatePasswordRegex("[", "telnet", "test.yaml")
		assert.Len(t, issues, 1)
		assert.Equal(t, LevelError, issues[0].Level)
		assert.Contains(t, issues[0].Message, "passwordRegex is not a valid regex")
	})

	t.Run("valid regex", func(t *testing.T) {
		issues := ValidatePasswordRegex("^root$", "ssh", "test.yaml")
		assert.Empty(t, issues)
	})
}
