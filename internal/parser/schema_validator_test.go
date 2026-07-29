package parser

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"

	"github.com/beelzebub-labs/beelzebub/v3/specs"
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
				ApiVersion: "v1",
				Protocol:   "ssh", Address: ":22",
				ServerVersion: "OpenSSH", PasswordRegex: "^(.+)$",
				Commands: []Command{{RegexStr: "^ls$", Handler: "files"}},
			},
		},
		{
			name: "valid HTTP",
			config: BeelzebubServiceConfiguration{
				ApiVersion: "v1",
				Protocol:   "http", Address: ":8080",
				Commands: []Command{{RegexStr: ".*", Handler: "ok"}},
			},
		},
		{
			name: "valid TCP no commands",
			config: BeelzebubServiceConfiguration{
				ApiVersion: "v1",
				Protocol:   "tcp", Address: ":3306",
				Banner: "8.0",
			},
		},
		{
			name: "valid MCP",
			config: BeelzebubServiceConfiguration{
				ApiVersion: "v1",
				Protocol:   "mcp", Address: ":8000",
				Tools: []Tool{
					{Name: "tool:test", Params: []Param{{Name: "arg", Description: "an arg"}}},
				},
			},
		},
		{
			name: "valid TELNET",
			config: BeelzebubServiceConfiguration{
				ApiVersion: "v1",
				Protocol:   "telnet", Address: ":23",
				PasswordRegex: "^(.+)$",
				Commands:      []Command{{RegexStr: "^ls$", Handler: "files"}},
			},
		},
		{
			name: "SSH with LLM",
			config: BeelzebubServiceConfiguration{
				ApiVersion: "v1",
				Protocol:   "ssh", Address: ":2222",
				ServerVersion: "OpenSSH", PasswordRegex: "^(.+)$",
				Commands: []Command{{RegexStr: "^(.+)$", Plugin: "LLMHoneypot"}},
				Plugin:   Plugin{LLMProvider: "openai", LLMModel: "gpt-4", OpenAISecretKey: "sk-..."},
			},
		},
		{
			name: "HTTP with Maze",
			config: BeelzebubServiceConfiguration{
				ApiVersion: "v1",
				Protocol:   "http", Address: ":8888",
				Commands: []Command{{RegexStr: ".*", Plugin: "MazeHoneypot"}},
			},
		},
		{
			name: "with rate limit",
			config: BeelzebubServiceConfiguration{
				ApiVersion: "v1",
				Protocol:   "ssh", Address: ":22",
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
			name: "missing apiVersion",
			config: BeelzebubServiceConfiguration{
				Protocol: "ssh", Address: ":22",
				ServerVersion: "OpenSSH", PasswordRegex: "^(.+)$",
				Commands: []Command{{RegexStr: "^ls$", Handler: "files"}},
			},
			msg: "apiVersion",
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
		ApiVersion: "v1",
		Protocol:   "ftp", Address: ":21",
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

func TestLoadSchemaRaw_MissingFile(t *testing.T) {
	value, err := loadSchemaRaw("does-not-exist.schema.json")
	assert.Error(t, err)
	assert.Nil(t, value)
}

func TestFlattenOutput_RootNestedChildren(t *testing.T) {
	schemaDoc, err := jsonschema.UnmarshalJSON(strings.NewReader(`{
		"type": "object",
		"properties": {
			"value": {"type": "string"}
		},
		"required": ["value"]
	}`))
	assert.NoError(t, err)

	compiler := jsonschema.NewCompiler()
	assert.NoError(t, compiler.AddResource("https://example.invalid/schema.json", schemaDoc))
	validator, err := compiler.Compile("https://example.invalid/schema.json")
	assert.NoError(t, err)

	instance, err := jsonschema.UnmarshalJSON(strings.NewReader(`{
		"value": true
	}`))
	assert.NoError(t, err)

	validationErr := validator.Validate(instance)
	assert.Error(t, validationErr)

	output := validationErr.(*jsonschema.ValidationError).DetailedOutput()
	issues := flattenOutput(output)
	assert.NotEmpty(t, issues)
	assert.Equal(t, LevelError, issues[0].Level)
	assert.Contains(t, issues[0].Message, "/value")
}

type schemaCompilerStub struct {
	addResourceErr error
	compileErr     error
}

func (s *schemaCompilerStub) AddResource(string, any) error { return s.addResourceErr }

func (s *schemaCompilerStub) Compile(string) (*jsonschema.Schema, error) {
	if s.compileErr != nil {
		return nil, s.compileErr
	}
	return &jsonschema.Schema{}, nil
}

type protocolErrorCompiler struct{ err error }

func (c *protocolErrorCompiler) AddResource(url string, _ any) error {
	if strings.Contains(url, "runtime-config") {
		return nil
	}
	return c.err
}

func (c *protocolErrorCompiler) Compile(url string) (*jsonschema.Schema, error) {
	if strings.Contains(url, "runtime-config") {
		return &jsonschema.Schema{}, nil
	}
	return nil, c.err
}

func withSchemaDependencies(t *testing.T, loader func(string) (any, error), compiler schemaCompiler) {
	t.Helper()
	oldLoader := loadSchema
	oldCompiler := newSchemaCompiler
	oldConfigToRawJSON := configToRawJSON
	loadSchema = loader
	newSchemaCompiler = func() schemaCompiler { return compiler }
	t.Cleanup(func() {
		loadSchema = oldLoader
		newSchemaCompiler = oldCompiler
		configToRawJSON = oldConfigToRawJSON
		ResetSchemaCache()
	})
	ResetSchemaCache()
}

func TestCompileAllSchemas_LoadAndCompileErrors(t *testing.T) {
	validDocument := func(string) (any, error) {
		return map[string]any{"type": "object"}, nil
	}

	t.Run("base schema load", func(t *testing.T) {
		withSchemaDependencies(t, func(string) (any, error) {
			return nil, errors.New("read failed")
		}, &schemaCompilerStub{})
		assert.EqualError(t, compileAllSchemas(), "loading base schema: read failed")
	})

	t.Run("protocol schema load", func(t *testing.T) {
		withSchemaDependencies(t, func(fileName string) (any, error) {
			if fileName != "runtime-config.schema.json" {
				return nil, errors.New("read failed")
			}
			return validDocument(fileName)
		}, &schemaCompilerStub{})
		err := compileAllSchemas()
		assert.Contains(t, err.Error(), "loading schema runtime-")
	})

	t.Run("base resource registration", func(t *testing.T) {
		withSchemaDependencies(t, validDocument, &schemaCompilerStub{addResourceErr: errors.New("register failed")})
		assert.EqualError(t, compileAllSchemas(), "registering base schema: register failed")
	})

	t.Run("protocol resource registration", func(t *testing.T) {
		withSchemaDependencies(t, validDocument, &protocolErrorCompiler{err: errors.New("register failed")})
		err := compileAllSchemas()
		assert.Contains(t, err.Error(), "registering schema runtime-")
	})

	t.Run("base compilation", func(t *testing.T) {
		withSchemaDependencies(t, validDocument, &schemaCompilerStub{compileErr: errors.New("compile failed")})
		assert.EqualError(t, compileAllSchemas(), "compiling base schema: compile failed")
	})

	t.Run("protocol compilation", func(t *testing.T) {
		withSchemaDependencies(t, validDocument, &protocolCompileErrorCompiler{err: errors.New("compile failed")})
		err := compileAllSchemas()
		assert.Contains(t, err.Error(), "compiling schema runtime-")
	})
}

type protocolCompileErrorCompiler struct{ err error }

func (c *protocolCompileErrorCompiler) AddResource(string, any) error { return nil }

func (c *protocolCompileErrorCompiler) Compile(url string) (*jsonschema.Schema, error) {
	if strings.Contains(url, "runtime-config") {
		return &jsonschema.Schema{}, nil
	}
	return nil, c.err
}

func TestValidateConfigSchema_InitializationAndConversionErrors(t *testing.T) {
	withSchemaDependencies(t, func(string) (any, error) {
		return nil, errors.New("embedded schema unavailable")
	}, &schemaCompilerStub{})
	issues := ValidateConfigSchema(BeelzebubServiceConfiguration{})
	assert.Contains(t, issues[0].Message, "schema initialization: loading base schema")

	withSchemaDependencies(t, func(string) (any, error) {
		return map[string]any{"type": "object"}, nil
	}, &schemaCompilerStub{})
	configToRawJSON = func(any) (any, error) { return nil, errors.New("conversion failed") }
	issues = ValidateConfigSchema(BeelzebubServiceConfiguration{})
	assert.Equal(t, "schema: converting config: conversion failed", issues[0].Message)
}

func writeEmbeddedSchema(t *testing.T, dir, name string) {
	t.Helper()
	data, err := specs.FS.ReadFile(name)
	assert.NoError(t, err)
	assert.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o644))
}

func writeBaseSchemaWithRequired(t *testing.T, dir string, required []string) {
	t.Helper()
	data, err := specs.FS.ReadFile("runtime-config.schema.json")
	assert.NoError(t, err)
	var doc map[string]any
	assert.NoError(t, json.Unmarshal(data, &doc))
	doc["required"] = required
	modified, err := json.Marshal(doc)
	assert.NoError(t, err)
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "runtime-config.schema.json"), modified, 0o644))
}

func TestSetSchemaDir(t *testing.T) {
	t.Cleanup(func() {
		assert.NoError(t, SetSchemaDir(""))
		ResetSchemaCache()
	})

	t.Run("empty dir restores embedded schemas", func(t *testing.T) {
		assert.NoError(t, SetSchemaDir(""))
		issues := ValidateConfigSchema(BeelzebubServiceConfiguration{
			ApiVersion: "v1",
			Protocol:   "ssh", Address: ":22",
			ServerVersion: "OpenSSH", PasswordRegex: "^(.+)$",
			Commands: []Command{{RegexStr: "^ls$", Handler: "files"}},
		})
		assert.Empty(t, issues)
	})

	t.Run("missing dir", func(t *testing.T) {
		err := SetSchemaDir(filepath.Join(t.TempDir(), "does-not-exist"))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "runtime-config.schema.json")
	})

	t.Run("dir without base schema", func(t *testing.T) {
		err := SetSchemaDir(t.TempDir())
		assert.Error(t, err)
	})

	t.Run("overrides embedded schemas", func(t *testing.T) {
		dir := t.TempDir()
		entries, err := specs.FS.ReadDir(".")
		assert.NoError(t, err)
		for _, entry := range entries {
			writeEmbeddedSchema(t, dir, entry.Name())
		}
		writeBaseSchemaWithRequired(t, dir, []string{"protocol", "address", "banner"})

		config := BeelzebubServiceConfiguration{
			ApiVersion: "v1",
			Protocol:   "ssh", Address: ":22",
			ServerVersion: "OpenSSH", PasswordRegex: "^(.+)$",
			Commands: []Command{{RegexStr: "^ls$", Handler: "files"}},
		}

		assert.NoError(t, SetSchemaDir(dir))
		issues := ValidateConfigSchema(config)
		assert.NotEmpty(t, issues)
		assert.Contains(t, issues[0].Message, "missing property 'banner'")

		assert.NoError(t, SetSchemaDir(""))
		assert.Empty(t, ValidateConfigSchema(config))
	})
}

func TestStructToRawJSON_MarshalError(t *testing.T) {
	value, err := structToRawJSON(func() {})
	assert.Error(t, err)
	assert.Nil(t, value)
}

func TestValidateConfigSchema_UnknownProtocolBaseSuccess(t *testing.T) {
	withSchemaDependencies(t, func(fileName string) (any, error) {
		if fileName == "runtime-config.schema.json" {
			return map[string]any{
				"type":     "object",
				"required": []any{"address"},
				"properties": map[string]any{
					"address": map[string]any{"type": "string", "minLength": 1},
				},
			}, nil
		}
		return map[string]any{"type": "object"}, nil
	}, jsonschema.NewCompiler())

	assert.Empty(t, ValidateConfigSchema(BeelzebubServiceConfiguration{
		Protocol: "unknown",
		Address:  ":1",
	}))
	issues := ValidateConfigSchema(BeelzebubServiceConfiguration{Protocol: "unknown"})
	assert.NotEmpty(t, issues)
	assert.Contains(t, issues[0].Message, "minLength")
}

func TestFlattenSchemaErrors_DetailedOutputFallback(t *testing.T) {
	issues := flattenDetailedSchemaErrors(errors.New("validation failed"), &jsonschema.OutputUnit{})
	assert.Equal(t, []ValidationIssue{{Level: LevelError, Message: "validation failed"}}, issues)
}
