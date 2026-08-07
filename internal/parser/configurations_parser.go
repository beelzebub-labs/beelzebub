// Package parser is responsible for parsing the configurations of the core and honeypot service
package parser

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// BeelzebubCoreConfigurations is the struct that contains the configurations of the core
type BeelzebubCoreConfigurations struct {
	Core struct {
		Logging        Logging        `yaml:"logging"`
		Tracings       Tracings       `yaml:"tracings"`
		Prometheus     Prometheus     `yaml:"prometheus"`
		BeelzebubCloud BeelzebubCloud `yaml:"beelzebub-cloud"`
	}
}

// Logging is the struct that contains the configurations of the logging
type Logging struct {
	Debug               bool   `yaml:"debug"`
	DebugReportCaller   bool   `yaml:"debugReportCaller"`
	LogDisableTimestamp bool   `yaml:"logDisableTimestamp"`
	LogsPath            string `yaml:"logsPath,omitempty"`
}

// Tracings is the struct that contains the configurations of the tracings
type Tracings struct {
	RabbitMQ `yaml:"rabbit-mq"`
}

type BeelzebubCloud struct {
	Enabled   bool   `yaml:"enabled"`
	URI       string `yaml:"uri"`
	AuthToken string `yaml:"auth-token"`
}
type RabbitMQ struct {
	Enabled bool   `yaml:"enabled"`
	URI     string `yaml:"uri"`
}
type Prometheus struct {
	Path string `yaml:"path"`
	Port string `yaml:"port"`
}

type Plugin struct {
	OpenAISecretKey         string `yaml:"openAISecretKey" json:"openAISecretKey,omitempty"`
	Host                    string `yaml:"host" json:"host,omitempty"`
	LLMModel                string `yaml:"llmModel" json:"llmModel,omitempty"`
	LLMProvider             string `yaml:"llmProvider" json:"llmProvider,omitempty"`
	Prompt                  string `yaml:"prompt" json:"prompt,omitempty"`
	InputValidationEnabled  bool   `yaml:"inputValidationEnabled" json:"inputValidationEnabled,omitempty"`
	InputValidationPrompt   string `yaml:"inputValidationPrompt" json:"inputValidationPrompt,omitempty"`
	OutputValidationEnabled bool   `yaml:"outputValidationEnabled" json:"outputValidationEnabled,omitempty"`
	OutputValidationPrompt  string `yaml:"outputValidationPrompt" json:"outputValidationPrompt,omitempty"`
	RateLimitEnabled        bool   `yaml:"rateLimitEnabled" json:"rateLimitEnabled,omitempty"`
	RateLimitRequests       int    `yaml:"rateLimitRequests" json:"rateLimitRequests,omitempty"`
	RateLimitWindowSeconds  int    `yaml:"rateLimitWindowSeconds" json:"rateLimitWindowSeconds,omitempty"`
}

// BeelzebubServiceConfiguration is the struct that contains the configurations of the honeypot service
type BeelzebubServiceConfiguration struct {
	Filename               string    `yaml:"-" json:"-"`
	ApiVersion             string    `yaml:"apiVersion" json:"apiVersion,omitempty"`
	Protocol               string    `yaml:"protocol" json:"protocol"`
	Address                string    `yaml:"address" json:"address"`
	Commands               []Command `yaml:"commands" json:"commands,omitempty"`
	Tools                  []Tool    `yaml:"tools" json:"tools,omitempty"`
	FallbackCommand        Command   `yaml:"fallbackCommand" json:"fallbackCommand,omitempty"`
	ServerVersion          string    `yaml:"serverVersion" json:"serverVersion,omitempty"`
	ServerName             string    `yaml:"serverName" json:"serverName,omitempty"`
	DeadlineTimeoutSeconds int       `yaml:"deadlineTimeoutSeconds" json:"deadlineTimeoutSeconds,omitempty"`
	PasswordRegex          string    `yaml:"passwordRegex" json:"passwordRegex,omitempty"`
	Description            string    `yaml:"description" json:"description,omitempty"`
	Banner                 string    `yaml:"banner" json:"banner,omitempty"`
	Plugin                 Plugin    `yaml:"plugin" json:"plugin,omitempty"`
	TLSCertPath            string    `yaml:"tlsCertPath" json:"tlsCertPath,omitempty"`
	TLSKeyPath             string    `yaml:"tlsKeyPath" json:"tlsKeyPath,omitempty"`
	// TrustedProxies is a list of CIDRs (or bare IPs) of upstream proxies whose
	// X-Forwarded-For / X-Real-IP headers can be trusted. When empty, those
	// headers are ignored and the immediate TCP peer is used as source IP.
	TrustedProxies     []string     `yaml:"trustedProxies,omitempty" json:"trustedProxies,omitempty"`
	TrustedProxiesNets []*net.IPNet `yaml:"-" json:"-"`
	// RawConfig is the original parsed document (from YAML or the
	// BEELZEBUB_SERVICES_CONFIG JSON) before unmarshalling into this struct.
	// Schema validation prefers it over the struct round-trip so that unknown
	// fields, explicit zero values and empty collections are not lost.
	RawConfig any `yaml:"-" json:"-"`
}

func (bsc BeelzebubServiceConfiguration) HashCode() (string, error) {
	data, err := json.Marshal(bsc)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

// Command is the struct that contains the configurations of the commands
type Command struct {
	RegexStr   string         `yaml:"regex" json:"regex,omitempty"`
	Regex      *regexp.Regexp `yaml:"-" json:"-"` // This field is parsed, not stored in the config itself.
	Methods    []string       `yaml:"methods" json:"methods,omitempty"`
	Handler    string         `yaml:"handler" json:"handler,omitempty"`
	Headers    []string       `yaml:"headers" json:"headers,omitempty"`
	StatusCode int            `yaml:"statusCode" json:"statusCode,omitempty"`
	Plugin     string         `yaml:"plugin" json:"plugin,omitempty"`
	Name       string         `yaml:"name" json:"name,omitempty"`
}

// Tool is the struct that contains the configurations of the MCP Honeypot
type Tool struct {
	Name        string           `yaml:"name" json:"name"`
	Description string           `yaml:"description" json:"description,omitempty"`
	Params      []Param          `yaml:"params" json:"params,omitempty"`
	Handler     string           `yaml:"handler" json:"handler,omitempty"`
	Annotations *ToolAnnotations `yaml:"annotations,omitempty" json:"annotations,omitempty"`
}

// ToolAnnotations contains MCP tool annotation hints for LLM clients
type ToolAnnotations struct {
	Title           string `yaml:"title,omitempty" json:"title,omitempty"`
	ReadOnlyHint    *bool  `yaml:"readOnlyHint,omitempty" json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `yaml:"destructiveHint,omitempty" json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `yaml:"idempotentHint,omitempty" json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `yaml:"openWorldHint,omitempty" json:"openWorldHint,omitempty"`
}

// Param is the struct that contains the configurations of the parameters of the tools
type Param struct {
	Name        string `yaml:"name" json:"name,omitempty"`
	Description string `yaml:"description" json:"description,omitempty"`
}

type configurationsParser struct {
	configurationsCorePath             string
	configurationsServicesDirectory    string
	readFileBytesByFilePathDependency  ReadFileBytesByFilePath
	gelAllFilesNameByDirNameDependency GelAllFilesNameByDirName
}

type ReadFileBytesByFilePath func(filePath string) ([]byte, error)

type GelAllFilesNameByDirName func(dirName string) ([]string, error)

// Init Parser, return a configurationsParser and use the D.I. Pattern to inject the dependencies
func Init(configurationsCorePath, configurationsServicesDirectory string) *configurationsParser {
	return &configurationsParser{
		configurationsCorePath:             configurationsCorePath,
		configurationsServicesDirectory:    configurationsServicesDirectory,
		readFileBytesByFilePathDependency:  readFileBytesByFilePath,
		gelAllFilesNameByDirNameDependency: gelAllFilesNameByDirName,
	}
}

// ReadConfigurationsCore is the method that reads the configurations of the core from files.
// If the file does not exist, a default empty configuration is used.
// Environment variables always override file values (see applyEnvOverrides).
func (bp configurationsParser) ReadConfigurationsCore() (*BeelzebubCoreConfigurations, error) {
	buf, err := bp.readFileBytesByFilePathDependency(bp.configurationsCorePath)
	if err != nil {
		if !isNotFound(err) {
			return nil, fmt.Errorf("in file %s: %v", bp.configurationsCorePath, err)
		}
		log.Debug("Core config file not found, falling back to environment variables")
		buf = []byte{}
	}

	beelzebubConfiguration := &BeelzebubCoreConfigurations{}
	if err = yaml.Unmarshal(buf, beelzebubConfiguration); err != nil {
		return nil, fmt.Errorf("in file %s: %v", bp.configurationsCorePath, err)
	}

	if err := applyEnvOverrides(beelzebubConfiguration); err != nil {
		return nil, fmt.Errorf("environment configuration: %v", err)
	}
	return beelzebubConfiguration, nil
}

// applyEnvOverrides overrides configuration fields with environment variable values when set.
// Supported variables:
//
//	BEELZEBUB_LOGGING_DEBUG, BEELZEBUB_LOGGING_DEBUG_REPORT_CALLER,
//	BEELZEBUB_LOGGING_LOG_DISABLE_TIMESTAMP, BEELZEBUB_LOGGING_LOGS_PATH,
//	BEELZEBUB_RABBITMQ_ENABLED, BEELZEBUB_RABBITMQ_URI,
//	BEELZEBUB_PROMETHEUS_PATH, BEELZEBUB_PROMETHEUS_PORT,
//	BEELZEBUB_CLOUD_ENABLED, BEELZEBUB_CLOUD_URI, BEELZEBUB_CLOUD_AUTH_TOKEN
func applyEnvOverrides(cfg *BeelzebubCoreConfigurations) error {
	parse := func(name, value string) (bool, error) {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return false, fmt.Errorf("%s must be a boolean: %w", name, err)
		}
		return parsed, nil
	}
	if v := os.Getenv("BEELZEBUB_LOGGING_DEBUG"); v != "" {
		parsed, err := parse("BEELZEBUB_LOGGING_DEBUG", v)
		if err != nil {
			return err
		}
		cfg.Core.Logging.Debug = parsed
	}
	if v := os.Getenv("BEELZEBUB_LOGGING_DEBUG_REPORT_CALLER"); v != "" {
		parsed, err := parse("BEELZEBUB_LOGGING_DEBUG_REPORT_CALLER", v)
		if err != nil {
			return err
		}
		cfg.Core.Logging.DebugReportCaller = parsed
	}
	if v := os.Getenv("BEELZEBUB_LOGGING_LOG_DISABLE_TIMESTAMP"); v != "" {
		parsed, err := parse("BEELZEBUB_LOGGING_LOG_DISABLE_TIMESTAMP", v)
		if err != nil {
			return err
		}
		cfg.Core.Logging.LogDisableTimestamp = parsed
	}
	if v := os.Getenv("BEELZEBUB_LOGGING_LOGS_PATH"); v != "" {
		cfg.Core.Logging.LogsPath = v
	}
	if v := os.Getenv("BEELZEBUB_RABBITMQ_ENABLED"); v != "" {
		parsed, err := parse("BEELZEBUB_RABBITMQ_ENABLED", v)
		if err != nil {
			return err
		}
		cfg.Core.Tracings.RabbitMQ.Enabled = parsed
	}
	if v := os.Getenv("BEELZEBUB_RABBITMQ_URI"); v != "" {
		cfg.Core.Tracings.RabbitMQ.URI = v
	}
	if v := os.Getenv("BEELZEBUB_PROMETHEUS_PATH"); v != "" {
		cfg.Core.Prometheus.Path = v
	}
	if v := os.Getenv("BEELZEBUB_PROMETHEUS_PORT"); v != "" {
		cfg.Core.Prometheus.Port = v
	}
	if v := os.Getenv("BEELZEBUB_CLOUD_ENABLED"); v != "" {
		parsed, err := parse("BEELZEBUB_CLOUD_ENABLED", v)
		if err != nil {
			return err
		}
		cfg.Core.BeelzebubCloud.Enabled = parsed
	}
	if v := os.Getenv("BEELZEBUB_CLOUD_URI"); v != "" {
		cfg.Core.BeelzebubCloud.URI = v
	}
	if v := os.Getenv("BEELZEBUB_CLOUD_AUTH_TOKEN"); v != "" {
		cfg.Core.BeelzebubCloud.AuthToken = v
	}
	return nil
}

func parseBool(v string) bool {
	b, _ := strconv.ParseBool(v)
	return b
}

func isNotFound(err error) bool {
	return os.IsNotExist(err)
}

// ReadConfigurationsServices is the method that reads the configurations of the honeypot services.
// If the BEELZEBUB_SERVICES_CONFIG environment variable is set (JSON array), it is used directly.
// Otherwise, service YAML files are loaded from the configured directory (existing behaviour).
func (bp configurationsParser) ReadConfigurationsServices() ([]BeelzebubServiceConfiguration, error) {
	services, _, err := bp.readConfigurationsServices(true)
	if err != nil {
		return nil, err
	}
	return services, nil
}

// ReadConfigurationsServicesForValidation reads service configurations in lenient mode:
// parse errors are collected as ValidationIssue instead of aborting.
func (bp configurationsParser) ReadConfigurationsServicesForValidation() ([]BeelzebubServiceConfiguration, []ValidationIssue, error) {
	return bp.readConfigurationsServices(false)
}

func (bp configurationsParser) readConfigurationsServices(strict bool) ([]BeelzebubServiceConfiguration, []ValidationIssue, error) {
	if envConfig := os.Getenv("BEELZEBUB_SERVICES_CONFIG"); envConfig != "" {
		return parseServicesFromEnv(envConfig, strict)
	}

	services, err := bp.gelAllFilesNameByDirNameDependency(bp.configurationsServicesDirectory)
	if err != nil {
		if isNotFound(err) {
			if strict {
				log.Warnf("Services config directory %q not found, falling back to empty configuration", bp.configurationsServicesDirectory)
				return []BeelzebubServiceConfiguration{}, nil, nil
			}
			return []BeelzebubServiceConfiguration{}, nil, nil
		}
		return nil, nil, fmt.Errorf("in directory %s: %v", bp.configurationsServicesDirectory, err)
	}

	var servicesConfiguration []BeelzebubServiceConfiguration
	var issues []ValidationIssue

	for _, servicesName := range services {
		filePath := filepath.Join(bp.configurationsServicesDirectory, servicesName)
		buf, err := bp.readFileBytesByFilePathDependency(filePath)
		if err != nil {
			if strict {
				return nil, nil, fmt.Errorf("in file %s: %v", filePath, err)
			}
			issues = append(issues, ValidationIssue{Level: LevelError, Message: err.Error(), Filename: servicesName})
			continue
		}

		beelzebubServiceConfiguration := &BeelzebubServiceConfiguration{}
		err = yaml.Unmarshal(buf, beelzebubServiceConfiguration)
		if err != nil {
			if strict {
				return nil, nil, fmt.Errorf("in file %s: %v", filePath, err)
			}
			issues = append(issues, ValidationIssue{Level: LevelError, Message: err.Error(), Filename: servicesName})
			continue
		}

		beelzebubServiceConfiguration.Filename = servicesName

		var rawDoc any
		if err := yaml.Unmarshal(buf, &rawDoc); err == nil {
			beelzebubServiceConfiguration.RawConfig = rawDoc
			if err := validateRawStringFields(rawDoc); err != nil {
				if strict {
					return nil, nil, fmt.Errorf("in file %s: %v", filePath, err)
				}
				issues = append(issues, ValidationIssue{Level: LevelError, Message: err.Error(), Filename: servicesName})
				continue
			}
		}

		if beelzebubServiceConfiguration.Plugin.RateLimitEnabled {
			if beelzebubServiceConfiguration.Plugin.RateLimitRequests <= 0 ||
				beelzebubServiceConfiguration.Plugin.RateLimitWindowSeconds <= 0 {
				if strict {
					return nil, nil, fmt.Errorf("in file %s: invalid rate limiting config: rateLimitRequests and rateLimitWindowSeconds must be > 0", filePath)
				}
				issues = append(issues, ValidationIssue{Level: LevelError, Message: "invalid rate limiting config: rateLimitRequests and rateLimitWindowSeconds must be > 0", Filename: servicesName})
				continue
			}
		}

		if err := beelzebubServiceConfiguration.CompileCommandRegex(); err != nil {
			if strict {
				return nil, nil, fmt.Errorf("in file %s: invalid regex: %v", filePath, err)
			}
			issues = append(issues, ValidationIssue{Level: LevelError, Message: fmt.Sprintf("invalid regex: %v", err), Filename: servicesName})
			continue
		}

		if err := beelzebubServiceConfiguration.CompileTrustedProxies(); err != nil {
			if strict {
				return nil, nil, fmt.Errorf("in file %s: %v", filePath, err)
			}
			issues = append(issues, ValidationIssue{Level: LevelError, Message: fmt.Sprintf("%v", err), Filename: servicesName})
			continue
		}

		log.Debug(beelzebubServiceConfiguration)

		servicesConfiguration = append(servicesConfiguration, *beelzebubServiceConfiguration)
	}

	return servicesConfiguration, issues, nil
}

// parseServicesFromEnv parses a JSON array of BeelzebubServiceConfiguration from
// the BEELZEBUB_SERVICES_CONFIG environment variable.
func parseServicesFromEnv(jsonStr string, strict bool) ([]BeelzebubServiceConfiguration, []ValidationIssue, error) {
	dec := json.NewDecoder(strings.NewReader(jsonStr))
	dec.UseNumber()
	var rawValue any
	if err := dec.Decode(&rawValue); err != nil {
		return nil, nil, fmt.Errorf("invalid BEELZEBUB_SERVICES_CONFIG: %v", err)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, nil, fmt.Errorf("invalid BEELZEBUB_SERVICES_CONFIG: %v", err)
	}
	rawServices, ok := rawValue.([]any)
	if !ok {
		return nil, nil, fmt.Errorf("invalid BEELZEBUB_SERVICES_CONFIG: expected a JSON array")
	}

	var issues []ValidationIssue
	var validServices []BeelzebubServiceConfiguration

	for i, rawService := range rawServices {
		filename := fmt.Sprintf("<env:BEELZEBUB_SERVICES_CONFIG>[%d]", i)
		if rawService == nil {
			message := "service entry must be a JSON object, not null"
			if strict {
				return nil, nil, fmt.Errorf("invalid BEELZEBUB_SERVICES_CONFIG[%d]: %s", i, message)
			}
			issues = append(issues, ValidationIssue{Level: LevelError, Message: message, Filename: filename})
			continue
		}
		data, err := json.Marshal(rawService)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid BEELZEBUB_SERVICES_CONFIG[%d]: %v", i, err)
		}
		var parsed BeelzebubServiceConfiguration
		if err := json.Unmarshal(data, &parsed); err != nil {
			if strict {
				return nil, nil, fmt.Errorf("invalid BEELZEBUB_SERVICES_CONFIG[%d]: %v", i, err)
			}
			issues = append(issues, ValidationIssue{Level: LevelError, Message: err.Error(), Filename: filename})
			continue
		}
		svc := &parsed
		svc.Filename = filename
		svc.RawConfig = rawService
		if err := validateRawStringFields(rawService); err != nil {
			if strict {
				return nil, nil, fmt.Errorf("invalid BEELZEBUB_SERVICES_CONFIG[%d]: %v", i, err)
			}
			issues = append(issues, ValidationIssue{Level: LevelError, Message: err.Error(), Filename: filename})
			continue
		}

		if svc.Plugin.RateLimitEnabled {
			if svc.Plugin.RateLimitRequests <= 0 || svc.Plugin.RateLimitWindowSeconds <= 0 {
				if strict {
					return nil, nil, fmt.Errorf("invalid rate limiting config in BEELZEBUB_SERVICES_CONFIG[%d]: rateLimitRequests and rateLimitWindowSeconds must be > 0", i)
				}
				issues = append(issues, ValidationIssue{Level: LevelError, Message: "invalid rate limiting config: rateLimitRequests and rateLimitWindowSeconds must be > 0", Filename: svc.Filename})
				continue
			}
		}

		if err := svc.CompileCommandRegex(); err != nil {
			if strict {
				return nil, nil, fmt.Errorf("invalid regex in BEELZEBUB_SERVICES_CONFIG[%d]: %v", i, err)
			}
			issues = append(issues, ValidationIssue{Level: LevelError, Message: fmt.Sprintf("invalid regex: %v", err), Filename: svc.Filename})
			continue
		}

		if err := svc.CompileTrustedProxies(); err != nil {
			if strict {
				return nil, nil, fmt.Errorf("in BEELZEBUB_SERVICES_CONFIG[%d]: %v", i, err)
			}
			issues = append(issues, ValidationIssue{Level: LevelError, Message: fmt.Sprintf("%v", err), Filename: svc.Filename})
			continue
		}

		validServices = append(validServices, *svc)
	}

	return validServices, issues, nil
}

// validateRawStringFields catches explicit nulls that yaml/json can otherwise
// decode into empty strings. Missing fields and empty arrays remain valid.
func validateRawStringFields(raw any) error {
	if raw == nil { // empty YAML document: preserve existing default behavior
		return nil
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("service entry must be a JSON/YAML object")
	}
	if value, present := object["trustedProxies"]; present {
		if err := validateStringArray(value, "trustedProxies"); err != nil {
			return err
		}
	}
	if commands, present := object["commands"]; present {
		if err := validateObjectArray(commands, "commands"); err != nil {
			return err
		}
		for i, item := range commands.([]any) {
			command := item.(map[string]any)
			if headers, present := command["headers"]; present {
				if err := validateStringArray(headers, fmt.Sprintf("commands[%d].headers", i)); err != nil {
					return err
				}
			}
		}
	}
	if tools, present := object["tools"]; present {
		if err := validateObjectArray(tools, "tools"); err != nil {
			return err
		}
		for i, item := range tools.([]any) {
			tool := item.(map[string]any)
			if params, present := tool["params"]; present {
				if err := validateObjectArray(params, fmt.Sprintf("tools[%d].params", i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateObjectArray(value any, field string) error {
	items, ok := value.([]any)
	if !ok {
		return fmt.Errorf("%s must be an array of objects", field)
	}
	for i, item := range items {
		if item == nil {
			return fmt.Errorf("%s[%d] must be an object, not null", field, i)
		}
		if _, ok := item.(map[string]any); !ok {
			return fmt.Errorf("%s[%d] must be an object", field, i)
		}
	}
	return nil
}

func validateStringArray(value any, field string) error {
	if value == nil {
		return fmt.Errorf("%s must be an array of strings, not null", field)
	}
	items, ok := value.([]any)
	if !ok {
		return fmt.Errorf("%s must be an array of strings", field)
	}
	for i, item := range items {
		if _, ok := item.(string); !ok {
			return fmt.Errorf("%s[%d] must be a string", field, i)
		}
	}
	return nil
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing data")
		}
		return err
	}
	return nil
}

// CompileCommandRegex is the method that compiles the regular expression for each configured Command.
func (c *BeelzebubServiceConfiguration) CompileCommandRegex() error {
	compiled := make([]*regexp.Regexp, len(c.Commands))
	for i, command := range c.Commands {
		if command.RegexStr != "" {
			rex, err := regexp.Compile(command.RegexStr)
			if err != nil {
				for j := range c.Commands {
					c.Commands[j].Regex = nil
				}
				return err
			}
			compiled[i] = rex
		}
	}
	for i := range c.Commands {
		c.Commands[i].Regex = compiled[i]
	}
	return nil
}

// CompileTrustedProxies parses the TrustedProxies entries (CIDRs or bare IPs)
// into net.IPNet values stored in TrustedProxiesNets. Bare IPs are treated as
// /32 (IPv4) or /128 (IPv6).
func (c *BeelzebubServiceConfiguration) CompileTrustedProxies() error {
	// Clear derived state first, so a failed recompilation cannot leave a
	// previous, potentially broader trust policy active.
	c.TrustedProxiesNets = nil
	nets := make([]*net.IPNet, 0, len(c.TrustedProxies))
	for _, entry := range c.TrustedProxies {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			ip := net.ParseIP(entry)
			if ip == nil {
				return fmt.Errorf("invalid trustedProxies entry %q", entry)
			}
			// To4 also matches IPv4-mapped IPv6; preserve the written family.
			if !strings.Contains(entry, ":") && ip.To4() != nil {
				entry += "/32"
			} else {
				entry += "/128"
			}
		}
		_, n, err := net.ParseCIDR(entry)
		if err != nil {
			return fmt.Errorf("invalid trustedProxies entry %q: %v", entry, err)
		}
		nets = append(nets, n)
	}
	c.TrustedProxiesNets = nets
	return nil
}

func gelAllFilesNameByDirName(dirName string) ([]string, error) {
	files, err := os.ReadDir(dirName)
	if err != nil {
		return nil, err
	}

	var filesName []string
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".yaml") {
			filesName = append(filesName, file.Name())
		}
	}
	sort.Strings(filesName)
	return filesName, nil
}

func readFileBytesByFilePath(filePath string) ([]byte, error) {
	return os.ReadFile(filePath)
}
