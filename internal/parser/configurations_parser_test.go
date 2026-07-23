package parser

import (
	"errors"
	"net"
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

func mockReadfilebytesConfigurationsCore(filePath string) ([]byte, error) {
	configurationsCoreBytes := []byte(`
core:
  logging:
    debug: false
    debugReportCaller: false
    logDisableTimestamp: true
    logsPath: ./logs
  tracings:
    rabbit-mq:
      enabled: true
      uri: "amqp://user:password@localhost/"
  beelzebub-cloud:
    enabled: true
    uri: "amqp://user:password@localhost/"
    auth-token: "iejfdjsl-aosdajosoidaj-dunfkjnfkjsdnkn"`)
	return configurationsCoreBytes, nil
}

func mockReadfilebytesFormatError(filePath string) ([]byte, error) {
	configurationsCoreBytes := []byte(`{{}`)
	return configurationsCoreBytes, nil
}

func mockReadfilebytesError(filePath string) ([]byte, error) {
	return nil, errors.New("mockErrorReadFileBytes")
}

func mockReadDirError(dirPath string) ([]string, error) {
	return nil, errors.New("mockErrorReadFileBytes")
}

func mockReadDirValid(dirPath string) ([]string, error) {
	return []string{""}, nil
}

func mockReadfilebytesBeelzebubServiceConfiguration(filePath string) ([]byte, error) {
	beelzebubServiceConfiguration := []byte(`
apiVersion: "v1"
protocol: "http"
address: ":8080"
tlsCertPath: "/tmp/cert.crt"
tlsKeyPath: "/tmp/cert.key"
tools:
  - name: "tool:user-account-manager"
    description: "Tool for querying and modifying user account details. Requires administrator privileges."
    params:
      - name: "user_id"
        description: "The ID of the user account to manage."
      - name: "action"
        description: "The action to perform on the user account, possible values are: get_details, reset_password, deactivate_account"
    handler: "reset_password ok"
commands:
  - regex: "wp-admin"
    handler: "login"
    headers:
      - "Content-Type: text/html"
  - name: "wp-admin"
    regex: "wp-admin"
    handler: "login"
    headers:
      - "Content-Type: text/html"
fallbackCommand:
  handler: "404 Not Found!"
  statusCode: 404
plugin:
  openAISecretKey: "qwerty"
  llmModel: "llama3"
  llmProvider: "ollama"
  host: "localhost:1563"
  prompt: "hello world"
  inputValidationEnabled: true
  inputValidationPrompt: "hello world"
  outputValidationEnabled: true
  outputValidationPrompt: "hello world"
`)
	return beelzebubServiceConfiguration, nil
}

func mockReadfilebytesBeelzebubServiceConfigurationDefaultValues(filePath string) ([]byte, error) {
	beelzebubServiceConfiguration := []byte(``)
	return beelzebubServiceConfiguration, nil
}

func mockReadfilebytesToolWithReadOnlyAnnotation(filePath string) ([]byte, error) {
	beelzebubServiceConfiguration := []byte(`
apiVersion: "v1"
protocol: "mcp"
address: ":8000"
tools:
  - name: "tool:query-logs"
    description: "Query system logs for analysis"
    annotations:
      title: "Query Logs"
      readOnlyHint: true
    params:
      - name: "filter"
        description: "Log filter criteria"
    handler: "log_query_handler"
`)
	return beelzebubServiceConfiguration, nil
}

func mockReadfilebytesToolWithDestructiveAnnotation(filePath string) ([]byte, error) {
	beelzebubServiceConfiguration := []byte(`
apiVersion: "v1"
protocol: "mcp"
address: ":8000"
tools:
  - name: "tool:delete-user"
    description: "Delete a user account permanently"
    annotations:
      title: "Delete User"
      destructiveHint: true
    params:
      - name: "user_id"
        description: "The user ID to delete"
    handler: "delete_user_handler"
`)
	return beelzebubServiceConfiguration, nil
}

func mockReadfilebytesToolWithMultipleAnnotations(filePath string) ([]byte, error) {
	beelzebubServiceConfiguration := []byte(`
apiVersion: "v1"
protocol: "mcp"
address: ":8000"
tools:
  - name: "tool:update-config"
    description: "Update system configuration"
    annotations:
      title: "Update Config"
      destructiveHint: true
      idempotentHint: true
      openWorldHint: false
    params:
      - name: "config_key"
        description: "Configuration key to update"
    handler: "update_config_handler"
`)
	return beelzebubServiceConfiguration, nil
}

func mockReadfilebytesNotFound(filePath string) ([]byte, error) {
	return nil, os.ErrNotExist
}

func TestReadConfigurationsCoreError(t *testing.T) {
	configurationsParser := Init("mockConfigurationsCorePath", "mockConfigurationsServicesDirectory")

	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesError
	beelzebubCoreConfigurations, err := configurationsParser.ReadConfigurationsCore()

	assert.Nil(t, beelzebubCoreConfigurations)
	assert.Error(t, err)
	assert.Equal(t, "in file mockConfigurationsCorePath: mockErrorReadFileBytes", err.Error())

	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesFormatError

	beelzebubCoreConfigurations, err = configurationsParser.ReadConfigurationsCore()
	assert.Nil(t, beelzebubCoreConfigurations)
	assert.Error(t, err)
	assert.Equal(t, "in file mockConfigurationsCorePath: yaml: line 1: did not find expected ',' or '}'", err.Error())
}

func TestReadConfigurationsCoreValid(t *testing.T) {
	configurationsParser := Init("", "")
	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesConfigurationsCore

	coreConfigurations, err := configurationsParser.ReadConfigurationsCore()
	assert.Nil(t, err)
	assert.NotNil(t, coreConfigurations.Core)
	assert.NotNil(t, coreConfigurations.Core.Logging)
	assert.Equal(t, coreConfigurations.Core.Logging.Debug, false)
	assert.Equal(t, coreConfigurations.Core.Logging.LogDisableTimestamp, true)
	assert.Equal(t, coreConfigurations.Core.Logging.DebugReportCaller, false)
	assert.Equal(t, coreConfigurations.Core.Logging.LogsPath, "./logs")
	assert.Equal(t, coreConfigurations.Core.Tracings.RabbitMQ.Enabled, true)
	assert.Equal(t, coreConfigurations.Core.Tracings.RabbitMQ.URI, "amqp://user:password@localhost/")
	assert.Equal(t, coreConfigurations.Core.BeelzebubCloud.Enabled, true)
	assert.Equal(t, coreConfigurations.Core.BeelzebubCloud.URI, "amqp://user:password@localhost/")
	assert.Equal(t, coreConfigurations.Core.BeelzebubCloud.AuthToken, "iejfdjsl-aosdajosoidaj-dunfkjnfkjsdnkn")
}

func TestReadConfigurationsServicesFail(t *testing.T) {
	configurationsParser := Init("", "")

	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesError
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirError

	beelzebubServiceConfiguration, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, beelzebubServiceConfiguration)
	assert.Error(t, err)
}

func TestReadConfigurationsServicesValid(t *testing.T) {
	configurationsParser := Init("", "")

	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesBeelzebubServiceConfiguration
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirValid

	beelzebubServicesConfiguration, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, err)

	firstBeelzebubServiceConfiguration := beelzebubServicesConfiguration[0]

	assert.Equal(t, firstBeelzebubServiceConfiguration.Protocol, "http")
	assert.Equal(t, firstBeelzebubServiceConfiguration.ApiVersion, "v1")
	assert.Equal(t, firstBeelzebubServiceConfiguration.Address, ":8080")
	assert.Equal(t, len(firstBeelzebubServiceConfiguration.Commands), 2)
	assert.Equal(t, len(firstBeelzebubServiceConfiguration.Commands), 2)
	assert.Equal(t, firstBeelzebubServiceConfiguration.Commands[0].RegexStr, "wp-admin")
	assert.Equal(t, firstBeelzebubServiceConfiguration.Commands[0].Regex.String(), "wp-admin")
	assert.Equal(t, firstBeelzebubServiceConfiguration.Commands[0].Handler, "login")
	assert.Equal(t, len(firstBeelzebubServiceConfiguration.Commands[0].Headers), 1)
	assert.Equal(t, firstBeelzebubServiceConfiguration.Commands[0].Headers[0], "Content-Type: text/html")
	assert.Equal(t, firstBeelzebubServiceConfiguration.Commands[1].Name, "wp-admin")
	assert.Equal(t, firstBeelzebubServiceConfiguration.FallbackCommand.Handler, "404 Not Found!")
	assert.Equal(t, firstBeelzebubServiceConfiguration.FallbackCommand.StatusCode, 404)
	assert.Equal(t, firstBeelzebubServiceConfiguration.Plugin.OpenAISecretKey, "qwerty")
	assert.Equal(t, firstBeelzebubServiceConfiguration.Plugin.LLMModel, "llama3")
	assert.Equal(t, firstBeelzebubServiceConfiguration.Plugin.LLMProvider, "ollama")
	assert.Equal(t, firstBeelzebubServiceConfiguration.Plugin.Host, "localhost:1563")
	assert.Equal(t, firstBeelzebubServiceConfiguration.Plugin.Prompt, "hello world")
	assert.Equal(t, firstBeelzebubServiceConfiguration.TLSCertPath, "/tmp/cert.crt")
	assert.Equal(t, firstBeelzebubServiceConfiguration.TLSKeyPath, "/tmp/cert.key")
	assert.Equal(t, firstBeelzebubServiceConfiguration.TLSKeyPath, "/tmp/cert.key")
	assert.Equal(t, len(firstBeelzebubServiceConfiguration.Tools), 1)
	assert.Equal(t, firstBeelzebubServiceConfiguration.Tools[0].Name, "tool:user-account-manager")
	assert.Equal(t, firstBeelzebubServiceConfiguration.Tools[0].Description, "Tool for querying and modifying user account details. Requires administrator privileges.")
	assert.Equal(t, len(firstBeelzebubServiceConfiguration.Tools[0].Params), 2)
	assert.Equal(t, firstBeelzebubServiceConfiguration.Tools[0].Params[0].Name, "user_id")
	assert.Equal(t, firstBeelzebubServiceConfiguration.Tools[0].Params[0].Description, "The ID of the user account to manage.")
	assert.Equal(t, firstBeelzebubServiceConfiguration.Tools[0].Handler, "reset_password ok")
}

func TestReadConfigurationsServicesGenerateHashCode(t *testing.T) {
	configurationsParser := Init("", "")

	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesBeelzebubServiceConfiguration
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirValid

	beelzebubServicesConfiguration, err := configurationsParser.ReadConfigurationsServices()

	hashCode, errHashCode := beelzebubServicesConfiguration[0].HashCode()

	assert.Nil(t, err)
	assert.Nil(t, errHashCode)
	assert.Equal(t, "34c8a2e81787e09f9e24efd6f60b2ceb808e47d9fa180ea8af9f054797ad4e00", hashCode)
}

func TestReadConfigurationsPluginGuardrailsValid(t *testing.T) {
	configurationsParser := Init("", "")

	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesBeelzebubServiceConfiguration
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirValid

	beelzebubServicesConfiguration, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, err)

	firstBeelzebubServiceConfiguration := beelzebubServicesConfiguration[0]

	assert.Equal(t, firstBeelzebubServiceConfiguration.Plugin.InputValidationEnabled, true)
	assert.Equal(t, firstBeelzebubServiceConfiguration.Plugin.InputValidationPrompt, "hello world")
	assert.Equal(t, firstBeelzebubServiceConfiguration.Plugin.OutputValidationEnabled, true)
	assert.Equal(t, firstBeelzebubServiceConfiguration.Plugin.OutputValidationPrompt, "hello world")
}

func TestReadConfigurationsDefaultValues(t *testing.T) {

	configurationsParser := Init("", "")

	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesBeelzebubServiceConfigurationDefaultValues
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirValid
	beelzebubServicesConfiguration, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, err)

	firstBeelzebubServiceConfiguration := beelzebubServicesConfiguration[0]
	assert.Equal(t, firstBeelzebubServiceConfiguration.Protocol, "")
	assert.Equal(t, firstBeelzebubServiceConfiguration.ApiVersion, "")
	assert.Equal(t, firstBeelzebubServiceConfiguration.Address, "")
	assert.Equal(t, len(firstBeelzebubServiceConfiguration.Commands), 0)
	assert.Equal(t, firstBeelzebubServiceConfiguration.FallbackCommand.Handler, "")
	assert.Equal(t, firstBeelzebubServiceConfiguration.FallbackCommand.StatusCode, 0)
	assert.Equal(t, firstBeelzebubServiceConfiguration.Plugin.OpenAISecretKey, "")
	assert.Equal(t, firstBeelzebubServiceConfiguration.Plugin.LLMModel, "")
	assert.Equal(t, firstBeelzebubServiceConfiguration.Plugin.LLMProvider, "")
	assert.Equal(t, firstBeelzebubServiceConfiguration.Plugin.Host, "")
	assert.Equal(t, firstBeelzebubServiceConfiguration.Plugin.Prompt, "")
	assert.Equal(t, firstBeelzebubServiceConfiguration.TLSCertPath, "")
	assert.Equal(t, firstBeelzebubServiceConfiguration.TLSKeyPath, "")
	assert.Equal(t, len(firstBeelzebubServiceConfiguration.Tools), 0)
	assert.Equal(t, firstBeelzebubServiceConfiguration.Plugin.InputValidationEnabled, false)
	assert.Equal(t, firstBeelzebubServiceConfiguration.Plugin.InputValidationPrompt, "")
	assert.Equal(t, firstBeelzebubServiceConfiguration.Plugin.OutputValidationEnabled, false)
	assert.Equal(t, firstBeelzebubServiceConfiguration.Plugin.OutputValidationPrompt, "")
}

func TestGelAllFilesNameByDirName(t *testing.T) {

	var dir = t.TempDir()

	files, err := gelAllFilesNameByDirName(dir)

	assert.Nil(t, err)
	assert.Equal(t, 0, len(files))
}

func TestGelAllFilesNameByDirNameFiles(t *testing.T) {

	var dir = t.TempDir()

	testFiles := []string{"file1.yaml", "file2.yaml", "file3.txt", "subdir", "file4.yaml"}
	for _, filename := range testFiles {
		filePath := dir + "/" + filename
		file, err := os.Create(filePath)
		assert.NoError(t, err)
		file.Close()
	}

	files, err := gelAllFilesNameByDirName(dir)

	assert.Nil(t, err)
	assert.Equal(t, 3, len(files))
}

func TestGelAllFilesNameByDirNameError(t *testing.T) {

	files, err := gelAllFilesNameByDirName("nosuchfile")

	assert.Nil(t, files)
	// Windows and Linux return slightly different error strings, but share a common prefix, so check for that.
	assert.Contains(t, err.Error(), "open nosuchfile: ")
}

func TestReadFileBytesByFilePath(t *testing.T) {

	var dir = t.TempDir()
	filePath := dir + "/test.yaml"

	f, err := os.Create(filePath)
	assert.NoError(t, err)
	f.Close()

	bytes, err := readFileBytesByFilePath(filePath)
	assert.NoError(t, err)

	assert.Equal(t, "", string(bytes))
}

func TestCompileCommandRegex(t *testing.T) {
	tests := []struct {
		name          string
		config        BeelzebubServiceConfiguration
		expectedError bool
	}{
		{
			name: "Valid Regex",
			config: BeelzebubServiceConfiguration{
				Commands: []Command{
					{RegexStr: "^/api/v1/.*$"},
					{RegexStr: "wp-admin"},
				},
			},
			expectedError: false,
		},
		{
			name: "Empty Regex",
			config: BeelzebubServiceConfiguration{
				Commands: []Command{
					{RegexStr: ""},
					{RegexStr: ""},
				},
			},
			expectedError: false,
		},
		{
			name: "Invalid Regex",
			config: BeelzebubServiceConfiguration{
				Commands: []Command{
					{RegexStr: "["},
				},
			},
			expectedError: true,
		},
		{
			name: "Mixed valid and Invalid Regex",
			config: BeelzebubServiceConfiguration{
				Commands: []Command{
					{RegexStr: "^/api/v1/.*$"},
					{RegexStr: "["},
					{RegexStr: "test"},
				},
			},
			expectedError: true,
		},
		{
			name:          "No commands",
			config:        BeelzebubServiceConfiguration{},
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.CompileCommandRegex()

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				for _, command := range tt.config.Commands {
					if command.RegexStr != "" {
						assert.NotNil(t, command.Regex)
						_, err := regexp.Compile(command.RegexStr)
						assert.NoError(t, err)

					} else {
						assert.Nil(t, command.Regex)
					}
				}
			}
		})
	}
}

func TestToolAnnotationsReadOnlyHint(t *testing.T) {
	configurationsParser := Init("", "")
	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesToolWithReadOnlyAnnotation
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirValid

	beelzebubServicesConfiguration, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, err)

	tool := beelzebubServicesConfiguration[0].Tools[0]
	assert.Equal(t, "tool:query-logs", tool.Name)
	assert.Equal(t, "Query system logs for analysis", tool.Description)

	// Verify annotations are parsed correctly
	assert.NotNil(t, tool.Annotations)
	assert.Equal(t, "Query Logs", tool.Annotations.Title)
	assert.NotNil(t, tool.Annotations.ReadOnlyHint)
	assert.True(t, *tool.Annotations.ReadOnlyHint)

	// Other hints should be nil (not specified in YAML)
	assert.Nil(t, tool.Annotations.DestructiveHint)
	assert.Nil(t, tool.Annotations.IdempotentHint)
	assert.Nil(t, tool.Annotations.OpenWorldHint)
}

func TestToolAnnotationsDestructiveHint(t *testing.T) {
	configurationsParser := Init("", "")
	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesToolWithDestructiveAnnotation
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirValid

	beelzebubServicesConfiguration, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, err)

	tool := beelzebubServicesConfiguration[0].Tools[0]
	assert.Equal(t, "tool:delete-user", tool.Name)

	// Verify annotations are parsed correctly
	assert.NotNil(t, tool.Annotations)
	assert.Equal(t, "Delete User", tool.Annotations.Title)
	assert.NotNil(t, tool.Annotations.DestructiveHint)
	assert.True(t, *tool.Annotations.DestructiveHint)

	// ReadOnlyHint should be nil (not specified)
	assert.Nil(t, tool.Annotations.ReadOnlyHint)
}

func TestToolAnnotationsMultipleHints(t *testing.T) {
	configurationsParser := Init("", "")
	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesToolWithMultipleAnnotations
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirValid

	beelzebubServicesConfiguration, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, err)

	tool := beelzebubServicesConfiguration[0].Tools[0]
	assert.Equal(t, "tool:update-config", tool.Name)

	// Verify all annotations are parsed correctly
	assert.NotNil(t, tool.Annotations)
	assert.Equal(t, "Update Config", tool.Annotations.Title)

	assert.NotNil(t, tool.Annotations.DestructiveHint)
	assert.True(t, *tool.Annotations.DestructiveHint)

	assert.NotNil(t, tool.Annotations.IdempotentHint)
	assert.True(t, *tool.Annotations.IdempotentHint)

	assert.NotNil(t, tool.Annotations.OpenWorldHint)
	assert.False(t, *tool.Annotations.OpenWorldHint)

	// ReadOnlyHint should be nil (not specified)
	assert.Nil(t, tool.Annotations.ReadOnlyHint)
}

func TestToolWithoutAnnotations(t *testing.T) {
	configurationsParser := Init("", "")
	// Uses existing mock that has tools without annotations
	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesBeelzebubServiceConfiguration
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirValid

	beelzebubServicesConfiguration, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, err)

	tool := beelzebubServicesConfiguration[0].Tools[0]
	assert.Equal(t, "tool:user-account-manager", tool.Name)

	// Annotations should be nil for backward compatibility
	assert.Nil(t, tool.Annotations)

	// Tool should still be fully functional
	assert.Equal(t, "Tool for querying and modifying user account details. Requires administrator privileges.", tool.Description)
	assert.Equal(t, 2, len(tool.Params))
	assert.Equal(t, "reset_password ok", tool.Handler)
}

func TestToolAnnotationsHashCodeStability(t *testing.T) {
	configurationsParser := Init("", "")
	// Use existing mock without annotations
	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesBeelzebubServiceConfiguration
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirValid

	beelzebubServicesConfiguration, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, err)

	// Existing hash from TestReadConfigurationsServicesGenerateHashCode
	// This ensures that adding the Annotations field with omitempty doesn't change
	// the hash for configs that don't use annotations
	hashCode, errHashCode := beelzebubServicesConfiguration[0].HashCode()
	assert.Nil(t, errHashCode)
	assert.Equal(t, "34c8a2e81787e09f9e24efd6f60b2ceb808e47d9fa180ea8af9f054797ad4e00", hashCode)
}

func TestReadConfigurationsCoreEnvOverridesFile(t *testing.T) {
	t.Setenv("BEELZEBUB_PROMETHEUS_PORT", ":9999")
	t.Setenv("BEELZEBUB_PROMETHEUS_PATH", "/custom-metrics")
	t.Setenv("BEELZEBUB_RABBITMQ_ENABLED", "true")
	t.Setenv("BEELZEBUB_RABBITMQ_URI", "amqp://env-host/")
	t.Setenv("BEELZEBUB_CLOUD_ENABLED", "true")
	t.Setenv("BEELZEBUB_CLOUD_URI", "https://env-cloud/")
	t.Setenv("BEELZEBUB_CLOUD_AUTH_TOKEN", "env-token")
	t.Setenv("BEELZEBUB_LOGGING_DEBUG", "true")
	t.Setenv("BEELZEBUB_LOGGING_LOGS_PATH", "/tmp/env-logs")

	configurationsParser := Init("", "")
	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesConfigurationsCore

	cfg, err := configurationsParser.ReadConfigurationsCore()
	assert.Nil(t, err)

	assert.Equal(t, ":9999", cfg.Core.Prometheus.Port)
	assert.Equal(t, "/custom-metrics", cfg.Core.Prometheus.Path)
	assert.Equal(t, true, cfg.Core.Tracings.RabbitMQ.Enabled)
	assert.Equal(t, "amqp://env-host/", cfg.Core.Tracings.RabbitMQ.URI)
	assert.Equal(t, true, cfg.Core.BeelzebubCloud.Enabled)
	assert.Equal(t, "https://env-cloud/", cfg.Core.BeelzebubCloud.URI)
	assert.Equal(t, "env-token", cfg.Core.BeelzebubCloud.AuthToken)
	assert.Equal(t, true, cfg.Core.Logging.Debug)
	assert.Equal(t, "/tmp/env-logs", cfg.Core.Logging.LogsPath)
}

func TestReadConfigurationsCoreEnvOnlyNoFile(t *testing.T) {
	t.Setenv("BEELZEBUB_PROMETHEUS_PORT", ":2112")
	t.Setenv("BEELZEBUB_PROMETHEUS_PATH", "/metrics")
	t.Setenv("BEELZEBUB_LOGGING_LOGS_PATH", "./logs")

	configurationsParser := Init("nonexistent.yaml", "")
	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesNotFound

	cfg, err := configurationsParser.ReadConfigurationsCore()
	assert.Nil(t, err)
	assert.Equal(t, ":2112", cfg.Core.Prometheus.Port)
	assert.Equal(t, "/metrics", cfg.Core.Prometheus.Path)
	assert.Equal(t, "./logs", cfg.Core.Logging.LogsPath)
}

func TestReadConfigurationsServicesFromEnv(t *testing.T) {
	t.Setenv("BEELZEBUB_SERVICES_CONFIG", `[{"apiVersion":"v1","protocol":"ssh","address":":2222","serverVersion":"OpenSSH","serverName":"ubuntu","passwordRegex":"^root$","deadlineTimeoutSeconds":60}]`)

	configurationsParser := Init("", "")

	services, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, err)
	assert.Len(t, services, 1)
	assert.Equal(t, "ssh", services[0].Protocol)
	assert.Equal(t, ":2222", services[0].Address)
	assert.Equal(t, "v1", services[0].ApiVersion)
	assert.Equal(t, "OpenSSH", services[0].ServerVersion)
	assert.Equal(t, "ubuntu", services[0].ServerName)
}

func TestReadConfigurationsServicesFromEnvInvalidJSON(t *testing.T) {
	t.Setenv("BEELZEBUB_SERVICES_CONFIG", `{invalid}`)

	configurationsParser := Init("", "")

	services, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, services)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid BEELZEBUB_SERVICES_CONFIG")
}

func TestReadConfigurationsServicesFromEnvInvalidRateLimit(t *testing.T) {
	t.Setenv("BEELZEBUB_SERVICES_CONFIG", `[{"protocol":"ssh","address":":2222","plugin":{"rateLimitEnabled":true,"rateLimitRequests":0,"rateLimitWindowSeconds":0}}]`)

	configurationsParser := Init("", "")

	services, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, services)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid rate limiting config")
}

func TestReadConfigurationsServicesFileFallbackWhenEnvAbsent(t *testing.T) {
	os.Unsetenv("BEELZEBUB_SERVICES_CONFIG")

	configurationsParser := Init("", "")
	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesBeelzebubServiceConfiguration
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirValid

	services, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, err)
	assert.Len(t, services, 1)
	assert.Equal(t, "http", services[0].Protocol)
}

func TestReadConfigurationsCoreEnvLoggingFields(t *testing.T) {
	t.Setenv("BEELZEBUB_LOGGING_DEBUG_REPORT_CALLER", "true")
	t.Setenv("BEELZEBUB_LOGGING_LOG_DISABLE_TIMESTAMP", "true")

	configurationsParser := Init("", "")
	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesConfigurationsCore

	cfg, err := configurationsParser.ReadConfigurationsCore()
	assert.Nil(t, err)
	assert.Equal(t, true, cfg.Core.Logging.DebugReportCaller)
	assert.Equal(t, true, cfg.Core.Logging.LogDisableTimestamp)
}

func TestReadConfigurationsServicesDirectoryNotFound(t *testing.T) {
	os.Unsetenv("BEELZEBUB_SERVICES_CONFIG")

	configurationsParser := Init("", "nonexistent-dir")
	configurationsParser.gelAllFilesNameByDirNameDependency = func(dirPath string) ([]string, error) {
		return nil, os.ErrNotExist
	}

	// Parser returns empty slice without error — the caller (main.go) is responsible
	// for rejecting empty services when cloud is also not enabled.
	services, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, err)
	assert.Empty(t, services)
}

func TestReadConfigurationsServicesFromEnvInvalidRegex(t *testing.T) {
	// Use the Go field JSON tag "regex" so that json.Unmarshal stores the value
	// as a plain string and leaves regex compilation to CompileCommandRegex.
	t.Setenv("BEELZEBUB_SERVICES_CONFIG", `[{"protocol":"ssh","address":":22","commands":[{"regex":"[invalid"}]}]`)

	configurationsParser := Init("", "")

	services, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, services)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid regex in BEELZEBUB_SERVICES_CONFIG")
}

func mockReadfilebytesWithRateLimitEnabled(filePath string) ([]byte, error) {
	return []byte(`
apiVersion: "v1"
protocol: "ssh"
address: ":22"
plugin:
  rateLimitEnabled: true
  rateLimitRequests: 0
  rateLimitWindowSeconds: 0
`), nil
}

func TestReadConfigurationsServicesRateLimitValidation(t *testing.T) {
	os.Unsetenv("BEELZEBUB_SERVICES_CONFIG")

	configurationsParser := Init("", "")
	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesWithRateLimitEnabled
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirValid

	services, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, services)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid rate limiting config")
}

func TestHashCode(t *testing.T) {
	conf := BeelzebubServiceConfiguration{
		Protocol: "http",
		Address:  ":8080",
	}

	hash, err := conf.HashCode()
	assert.NoError(t, err)
	assert.NotEmpty(t, hash)

	// Same config produces same hash
	hash2, err := conf.HashCode()
	assert.NoError(t, err)
	assert.Equal(t, hash, hash2)

	// Different config produces different hash
	conf2 := BeelzebubServiceConfiguration{
		Protocol: "ssh",
		Address:  ":22",
	}
	hash3, err := conf2.HashCode()
	assert.NoError(t, err)
	assert.NotEqual(t, hash, hash3)
}

func TestCompileTrustedProxies(t *testing.T) {
	tests := []struct {
		name           string
		entries        []string
		expectErr      bool
		expectedCount  int
		shouldContain  []string
		shouldNotMatch []string
	}{
		{
			name:          "Empty list",
			entries:       nil,
			expectedCount: 0,
		},
		{
			name:          "Whitespace and empty entries are skipped",
			entries:       []string{"  ", "", "10.0.0.0/8"},
			expectedCount: 1,
			shouldContain: []string{"10.5.5.5"},
		},
		{
			name:          "Bare IPv4 becomes /32",
			entries:       []string{"127.0.0.1"},
			expectedCount: 1,
			shouldContain: []string{"127.0.0.1"},
			shouldNotMatch: []string{
				"127.0.0.2",
			},
		},
		{
			name:          "Bare IPv6 becomes /128",
			entries:       []string{"fd00::1"},
			expectedCount: 1,
			shouldContain: []string{"fd00::1"},
			shouldNotMatch: []string{
				"fd00::2",
			},
		},
		{
			name:          "Valid CIDRs",
			entries:       []string{"172.16.0.0/12", "10.0.0.0/8"},
			expectedCount: 2,
			shouldContain: []string{"172.20.0.5", "10.255.0.1"},
			shouldNotMatch: []string{
				"192.168.0.1",
			},
		},
		{
			name:          "Trims whitespace around entries",
			entries:       []string{"  192.168.1.0/24  "},
			expectedCount: 1,
			shouldContain: []string{"192.168.1.42"},
		},
		{
			name:      "Invalid CIDR returns error",
			entries:   []string{"not-a-cidr"},
			expectErr: true,
		},
		{
			name:      "Invalid mask returns error",
			entries:   []string{"10.0.0.0/99"},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := BeelzebubServiceConfiguration{TrustedProxies: tt.entries}
			err := c.CompileTrustedProxies()
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Len(t, c.TrustedProxiesNets, tt.expectedCount)
			for _, ipStr := range tt.shouldContain {
				ip := net.ParseIP(ipStr)
				matched := false
				for _, n := range c.TrustedProxiesNets {
					if n.Contains(ip) {
						matched = true
						break
					}
				}
				assert.True(t, matched, "expected %s to match a compiled CIDR", ipStr)
			}
			for _, ipStr := range tt.shouldNotMatch {
				ip := net.ParseIP(ipStr)
				for _, n := range c.TrustedProxiesNets {
					assert.False(t, n.Contains(ip), "did not expect %s to match %s", ipStr, n.String())
				}
			}
		})
	}
}

func mockReadfilebytesWithTrustedProxies(filePath string) ([]byte, error) {
	return []byte(`
apiVersion: "v1"
protocol: "http"
address: ":80"
trustedProxies:
  - "172.16.0.0/12"
  - "10.0.0.5"
`), nil
}

func TestReadConfigurationsServicesParsesTrustedProxies(t *testing.T) {
	configurationsParser := Init("", "")
	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesWithTrustedProxies
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirValid

	services, err := configurationsParser.ReadConfigurationsServices()
	assert.NoError(t, err)
	assert.Len(t, services, 1)
	assert.Equal(t, []string{"172.16.0.0/12", "10.0.0.5"}, services[0].TrustedProxies)
	assert.Len(t, services[0].TrustedProxiesNets, 2)
}

func mockReadfilebytesWithInvalidTrustedProxies(filePath string) ([]byte, error) {
	return []byte(`
apiVersion: "v1"
protocol: "http"
address: ":80"
trustedProxies:
  - "not-a-cidr"
`), nil
}

func TestReadConfigurationsServicesRejectsInvalidTrustedProxies(t *testing.T) {
	configurationsParser := Init("", "")
	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesWithInvalidTrustedProxies
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirValid

	services, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, services)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid trustedProxies entry")
}

func TestReadConfigurationsServicesFromEnvParsesTrustedProxies(t *testing.T) {
	t.Setenv("BEELZEBUB_SERVICES_CONFIG", `[{"apiVersion":"v1","protocol":"http","address":":80","TrustedProxies":["172.16.0.0/12"]}]`)

	configurationsParser := Init("", "")

	services, err := configurationsParser.ReadConfigurationsServices()
	assert.NoError(t, err)
	assert.Len(t, services, 1)
	assert.Equal(t, []string{"172.16.0.0/12"}, services[0].TrustedProxies)
	assert.Len(t, services[0].TrustedProxiesNets, 1)
}

func TestReadConfigurationsServicesFromEnvRejectsInvalidTrustedProxies(t *testing.T) {
	t.Setenv("BEELZEBUB_SERVICES_CONFIG", `[{"apiVersion":"v1","protocol":"http","address":":80","TrustedProxies":["bogus"]}]`)

	configurationsParser := Init("", "")

	services, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, services)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid trustedProxies entry")
}

func mockReadfilebytesWithInvalidRegex(filePath string) ([]byte, error) {
	return []byte(`
apiVersion: "v1"
protocol: "http"
address: ":8080"
commands:
  - regex: "[invalid"
    handler: "login"
`), nil
}

func TestReadConfigurationsServicesInvalidRegex(t *testing.T) {
	os.Unsetenv("BEELZEBUB_SERVICES_CONFIG")

	configurationsParser := Init("", "")
	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesWithInvalidRegex
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirValid

	services, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, services)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid regex")
}

func mockReadDirWithFilename(dirPath string) ([]string, error) {
	return []string{"test-service.yaml"}, nil
}

func mockReadDirMultipleFiles(dirPath string) ([]string, error) {
	return []string{"valid.yaml", "invalid-yaml.yaml", "invalid-regex.yaml"}, nil
}

func mockReadFileBytesForValidation(filePath string) ([]byte, error) {
	switch filePath {
	case "valid.yaml":
		return mockReadfilebytesBeelzebubServiceConfiguration(filePath)
	case "invalid-yaml.yaml":
		return []byte(`{{}`), nil
	case "invalid-regex.yaml":
		return []byte(`
apiVersion: "v1"
protocol: "http"
address: ":8080"
commands:
  - regex: "[invalid"
    handler: "login"
`), nil
	}
	return nil, os.ErrNotExist
}

func TestReadConfigurationsServicesFilenameIsSet(t *testing.T) {
	os.Unsetenv("BEELZEBUB_SERVICES_CONFIG")

	configurationsParser := Init("", "")
	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesBeelzebubServiceConfiguration
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirWithFilename

	services, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, err)
	assert.Len(t, services, 1)
	assert.Equal(t, "test-service.yaml", services[0].Filename)
}

func TestReadConfigurationsServicesForValidationNoErrors(t *testing.T) {
	os.Unsetenv("BEELZEBUB_SERVICES_CONFIG")

	configurationsParser := Init("", "")
	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesBeelzebubServiceConfiguration
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirWithFilename

	services, issues, err := configurationsParser.ReadConfigurationsServicesForValidation()
	assert.Nil(t, err)
	assert.Len(t, services, 1)
	assert.Empty(t, issues)
	assert.Equal(t, "test-service.yaml", services[0].Filename)
}

func TestReadConfigurationsServicesForValidationWithErrors(t *testing.T) {
	os.Unsetenv("BEELZEBUB_SERVICES_CONFIG")

	configurationsParser := Init("", "")
	configurationsParser.readFileBytesByFilePathDependency = mockReadFileBytesForValidation
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirMultipleFiles

	services, issues, err := configurationsParser.ReadConfigurationsServicesForValidation()
	assert.Nil(t, err)
	assert.Len(t, services, 1)
	assert.Len(t, issues, 2)
	assert.Equal(t, "valid.yaml", services[0].Filename)
	assert.Equal(t, "invalid-yaml.yaml", issues[0].Filename)
	assert.Equal(t, LevelError, issues[0].Level)
	assert.Equal(t, "invalid-regex.yaml", issues[1].Filename)
	assert.Equal(t, LevelError, issues[1].Level)
}

func TestReadConfigurationsServicesForValidationFromEnv(t *testing.T) {
	t.Setenv("BEELZEBUB_SERVICES_CONFIG", `[{"apiVersion":"v1","protocol":"ssh","address":":2222"}]`)

	configurationsParser := Init("", "")

	services, issues, err := configurationsParser.ReadConfigurationsServicesForValidation()
	assert.Nil(t, err)
	assert.Len(t, services, 1)
	assert.Empty(t, issues)
	assert.Equal(t, "<env:BEELZEBUB_SERVICES_CONFIG>[0]", services[0].Filename)
	assert.Equal(t, "ssh", services[0].Protocol)
}

func TestReadConfigurationsServicesForValidationWithRateLimitError(t *testing.T) {
	os.Unsetenv("BEELZEBUB_SERVICES_CONFIG")

	configurationsParser := Init("", "")
	configurationsParser.readFileBytesByFilePathDependency = func(filePath string) ([]byte, error) {
		return []byte(`
apiVersion: "v1"
protocol: "http"
address: ":8080"
commands:
  - regex: "^(.+)$"
plugin:
  rateLimitEnabled: true
  rateLimitRequests: 0
  rateLimitWindowSeconds: 0
`), nil
	}
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirWithFilename

	services, issues, err := configurationsParser.ReadConfigurationsServicesForValidation()
	assert.Nil(t, err)
	assert.Empty(t, services)
	assert.Len(t, issues, 1)
	assert.Equal(t, LevelError, issues[0].Level)
	assert.Contains(t, issues[0].Message, "invalid rate limiting config")
}

func TestReadConfigurationsServicesForValidationFromEnvInvalidJSON(t *testing.T) {
	t.Setenv("BEELZEBUB_SERVICES_CONFIG", `{invalid}`)

	configurationsParser := Init("", "")

	services, issues, err := configurationsParser.ReadConfigurationsServicesForValidation()
	assert.Error(t, err)
	assert.Nil(t, services)
	assert.Nil(t, issues)
	assert.Contains(t, err.Error(), "invalid BEELZEBUB_SERVICES_CONFIG")
}

func TestReadConfigurationsServicesForValidationDirError(t *testing.T) {
	os.Unsetenv("BEELZEBUB_SERVICES_CONFIG")

	configurationsParser := Init("", "")
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirError

	services, issues, err := configurationsParser.ReadConfigurationsServicesForValidation()
	assert.Nil(t, services)
	assert.Nil(t, issues)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mockErrorReadFileBytes")
}

func TestReadConfigurationsServicesForValidationDirNotFound(t *testing.T) {
	os.Unsetenv("BEELZEBUB_SERVICES_CONFIG")

	configurationsParser := Init("", "")
	configurationsParser.gelAllFilesNameByDirNameDependency = func(dirPath string) ([]string, error) {
		return nil, os.ErrNotExist
	}

	services, issues, err := configurationsParser.ReadConfigurationsServicesForValidation()
	assert.Nil(t, err)
	assert.Empty(t, services)
	assert.Empty(t, issues)
}

func TestReadConfigurationsServicesForValidationFileReadError(t *testing.T) {
	os.Unsetenv("BEELZEBUB_SERVICES_CONFIG")

	configurationsParser := Init("", "")
	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesError
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirWithFilename

	services, issues, err := configurationsParser.ReadConfigurationsServicesForValidation()
	assert.Nil(t, err)
	assert.Empty(t, services)
	assert.Len(t, issues, 1)
	assert.Equal(t, LevelError, issues[0].Level)
	assert.Equal(t, "test-service.yaml", issues[0].Filename)
}

func TestReadConfigurationsServicesForValidationFromEnvRateLimitError(t *testing.T) {
	t.Setenv("BEELZEBUB_SERVICES_CONFIG", `[{"protocol":"ssh","address":":2222","plugin":{"rateLimitEnabled":true,"rateLimitRequests":0,"rateLimitWindowSeconds":0}}]`)

	configurationsParser := Init("", "")

	services, issues, err := configurationsParser.ReadConfigurationsServicesForValidation()
	assert.Nil(t, err)
	assert.Empty(t, services)
	assert.Len(t, issues, 1)
	assert.Equal(t, LevelError, issues[0].Level)
	assert.Contains(t, issues[0].Message, "invalid rate limiting config")
	assert.Equal(t, "<env:BEELZEBUB_SERVICES_CONFIG>[0]", issues[0].Filename)
}

func TestReadConfigurationsServicesForValidationFromEnvInvalidRegex(t *testing.T) {
	t.Setenv("BEELZEBUB_SERVICES_CONFIG", `[{"protocol":"ssh","address":":22","commands":[{"regex":"[invalid"}]}]`)

	configurationsParser := Init("", "")

	services, issues, err := configurationsParser.ReadConfigurationsServicesForValidation()
	assert.Nil(t, err)
	assert.Empty(t, services)
	assert.Len(t, issues, 1)
	assert.Equal(t, LevelError, issues[0].Level)
	assert.Contains(t, issues[0].Message, "invalid regex")
	assert.Equal(t, "<env:BEELZEBUB_SERVICES_CONFIG>[0]", issues[0].Filename)
}

func TestReadConfigurationsServicesStrictFileReadError(t *testing.T) {
	os.Unsetenv("BEELZEBUB_SERVICES_CONFIG")

	configurationsParser := Init("", "")
	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesError
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirWithFilename

	services, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, services)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mockErrorReadFileBytes")
}

func TestReadConfigurationsServicesStrictYamlError(t *testing.T) {
	os.Unsetenv("BEELZEBUB_SERVICES_CONFIG")

	configurationsParser := Init("", "")
	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesFormatError
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirWithFilename

	services, err := configurationsParser.ReadConfigurationsServices()
	assert.Nil(t, services)
	assert.Error(t, err)
}

func TestReadConfigurationsServicesForValidationInvalidTrustedProxies(t *testing.T) {
	os.Unsetenv("BEELZEBUB_SERVICES_CONFIG")

	configurationsParser := Init("", "")
	configurationsParser.readFileBytesByFilePathDependency = mockReadfilebytesWithInvalidTrustedProxies
	configurationsParser.gelAllFilesNameByDirNameDependency = mockReadDirWithFilename

	services, issues, err := configurationsParser.ReadConfigurationsServicesForValidation()
	assert.Nil(t, err)
	assert.Empty(t, services)
	assert.Len(t, issues, 1)
	assert.Equal(t, LevelError, issues[0].Level)
	assert.Contains(t, issues[0].Message, "invalid trustedProxies entry")
	assert.Equal(t, "test-service.yaml", issues[0].Filename)
}

func TestReadConfigurationsServicesForValidationFromEnvInvalidTrustedProxies(t *testing.T) {
	t.Setenv("BEELZEBUB_SERVICES_CONFIG", `[{"apiVersion":"v1","protocol":"http","address":":80","TrustedProxies":["bogus"]}]`)

	configurationsParser := Init("", "")

	services, issues, err := configurationsParser.ReadConfigurationsServicesForValidation()
	assert.Nil(t, err)
	assert.Empty(t, services)
	assert.Len(t, issues, 1)
	assert.Equal(t, LevelError, issues[0].Level)
	assert.Contains(t, issues[0].Message, "invalid trustedProxies entry")
	assert.Equal(t, "<env:BEELZEBUB_SERVICES_CONFIG>[0]", issues[0].Filename)
}
