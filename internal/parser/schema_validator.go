package parser

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/beelzebub-labs/beelzebub/v3/specs"
)

// SchemaValidator validates service configurations against the per-protocol
// JSON Schemas embedded in the binary. Registered as a ServiceValidator so it
// runs automatically during Validate().
type SchemaValidator struct{}

func (v *SchemaValidator) Name() string {
	return "schema"
}

func (v *SchemaValidator) Validate(config BeelzebubServiceConfiguration) []ValidationIssue {
	return ValidateConfigSchema(config)
}

var (
	compileSchemasOnce sync.Once
	baseSchema         *jsonschema.Schema
	protocolSchemas    map[string]*jsonschema.Schema
	schemaInitErr      error
)

// protocolSchemaURLs maps protocol name to the per-protocol schema $id.
var protocolSchemaURLs = map[string]string{
	"ssh":    "https://beelzebub-labs.github.io/specs/runtime-ssh.schema.json",
	"http":   "https://beelzebub-labs.github.io/specs/runtime-http.schema.json",
	"tcp":    "https://beelzebub-labs.github.io/specs/runtime-tcp.schema.json",
	"telnet": "https://beelzebub-labs.github.io/specs/runtime-telnet.schema.json",
	"mcp":    "https://beelzebub-labs.github.io/specs/runtime-mcp.schema.json",
}

// ResetSchemaCache clears the compiled schema cache. Used in tests.
func ResetSchemaCache() {
	compileSchemasOnce = sync.Once{}
	baseSchema = nil
	protocolSchemas = nil
	schemaInitErr = nil
}

func compileAllSchemas() error {
	compileSchemasOnce.Do(func() {
		compiler := jsonschema.NewCompiler()

		baseDoc, err := loadSchemaRaw("runtime-config.schema.json")
		if err != nil {
			schemaInitErr = fmt.Errorf("loading base schema: %w", err)
			return
		}
		baseURL := "https://beelzebub-labs.github.io/specs/runtime-config.schema.json"
		if err := compiler.AddResource(baseURL, baseDoc); err != nil {
			schemaInitErr = fmt.Errorf("registering base schema: %w", err)
			return
		}
		bs, err := compiler.Compile(baseURL)
		if err != nil {
			schemaInitErr = fmt.Errorf("compiling base schema: %w", err)
			return
		}
		baseSchema = bs

		schemas := make(map[string]*jsonschema.Schema, len(protocolSchemaURLs))
		for proto, url := range protocolSchemaURLs {
			fileName := fmt.Sprintf("runtime-%s.schema.json", proto)
			doc, err := loadSchemaRaw(fileName)
			if err != nil {
				schemaInitErr = fmt.Errorf("loading schema %s: %w", fileName, err)
				return
			}
			if err := compiler.AddResource(url, doc); err != nil {
				schemaInitErr = fmt.Errorf("registering schema %s: %w", fileName, err)
				return
			}
			s, err := compiler.Compile(url)
			if err != nil {
				schemaInitErr = fmt.Errorf("compiling schema %s: %w", fileName, err)
				return
			}
			schemas[proto] = s
		}

		protocolSchemas = schemas
	})
	return schemaInitErr
}

// loadSchemaRaw reads a JSON Schema file from the embedded specs/ FS
// and returns it as a raw JSON value (any) for the compiler.
func loadSchemaRaw(fileName string) (any, error) {
	data, err := specs.FS.ReadFile(fileName)
	if err != nil {
		return nil, err
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(data))
}

// ValidateConfigSchema validates a BeelzebubServiceConfiguration against
// the JSON Schema. For known protocols, validates against the per-protocol
// schema (which includes the base schema via $ref). For unknown protocols,
// validates against the base schema only.
func ValidateConfigSchema(config BeelzebubServiceConfiguration) []ValidationIssue {
	if err := compileAllSchemas(); err != nil {
		return []ValidationIssue{{
			Level:   LevelError,
			Message: fmt.Sprintf("schema initialization: %v", err),
		}}
	}

	doc, err := structToRawJSON(config)
	if err != nil {
		return []ValidationIssue{{
			Level:   LevelError,
			Message: fmt.Sprintf("schema: converting config: %v", err),
		}}
	}

	// For known protocols, validate against the per-protocol schema.
	// It extends the base via $ref, so base constraints are included.
	if validator, ok := protocolSchemas[config.Protocol]; ok {
		if err := validator.Validate(doc); err != nil {
			return flattenSchemaErrors(err)
		}
		return nil
	}

	// For unknown protocols, validate against the base schema only.
	if err := baseSchema.Validate(doc); err != nil {
		return flattenSchemaErrors(err)
	}
	return nil
}

// structToRawJSON converts a Go struct to a raw JSON value (any) via JSON
// marshal/unmarshal. This is needed because jsonschema works with JSON types.
func structToRawJSON(v any) (any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(data))
}

// flattenSchemaErrors converts a jsonschema ValidationError tree into a flat
// list of human-readable ValidationIssues.
func flattenSchemaErrors(err error) []ValidationIssue {
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []ValidationIssue{{
			Level:   LevelError,
			Message: fmt.Sprintf("schema: %v", err),
		}}
	}

	out := ve.DetailedOutput()
	issues := flattenOutput(out)

	if len(issues) == 0 {
		issues = append(issues, ValidationIssue{
			Level:   LevelError,
			Message: err.Error(),
		})
	}
	return issues
}

func flattenOutput(unit *jsonschema.OutputUnit) []ValidationIssue {
	var issues []ValidationIssue
	if unit.Error != nil {
		loc := unit.InstanceLocation
		if loc == "" {
			loc = "/"
		}
		msg := unit.Error.String()
		if loc != "/" {
			msg = loc + " " + msg
		}
		issues = append(issues, ValidationIssue{
			Level:   LevelError,
			Message: msg,
		})
	}
	for i := range unit.Errors {
		issues = append(issues, flattenOutput(&unit.Errors[i])...)
	}
	return issues
}

func init() {
	RegisterServiceValidator(&SchemaValidator{})
}
