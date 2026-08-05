package parser

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/beelzebub-labs/beelzebub/v3/specs"
)

// SchemaValidator validates service configurations against the per-protocol
// JSON Schemas embedded in the binary. Registered as a ServiceValidator so it
// runs automatically during Validate().
type SchemaValidator struct{}

type schemaCompiler interface {
	AddResource(string, any) error
	Compile(string) (*jsonschema.Schema, error)
}

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
	newSchemaCompiler  = func() schemaCompiler { return jsonschema.NewCompiler() }
	loadSchema         = loadSchemaRaw
	configToRawJSON    = configToRawJSONDefault
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
		compiler := newSchemaCompiler()

		baseDoc, err := loadSchema("runtime-config.schema.json")
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
			doc, err := loadSchema(fileName)
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

// SetSchemaDir switches the schema source from the embedded specs/ FS to the
// JSON Schema files in the given directory, and resets the compiled schema
// cache. Pass an empty string to restore the embedded schemas.
func SetSchemaDir(dir string) error {
	if dir == "" {
		loadSchema = loadSchemaRaw
		ResetSchemaCache()
		return nil
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(absDir, "runtime-config.schema.json")); err != nil {
		return fmt.Errorf("specs dir %q: %w", absDir, err)
	}
	loadSchema = func(fileName string) (any, error) {
		data, err := os.ReadFile(filepath.Join(absDir, fileName))
		if err != nil {
			return nil, err
		}
		return jsonschema.UnmarshalJSON(bytes.NewReader(data))
	}
	ResetSchemaCache()
	return nil
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

	doc, err := configToRawJSON(config)
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

// configToRawJSONDefault converts a service configuration to a raw JSON value
// (any) for schema validation. When the configuration carries the original
// parsed document (RawConfig), it is used as-is so that unknown fields,
// explicit zero values and empty collections are preserved; otherwise the Go
// struct is serialized.
func configToRawJSONDefault(v any) (any, error) {
	if cfg, ok := v.(BeelzebubServiceConfiguration); ok && cfg.RawConfig != nil {
		return marshalToRawJSON(cfg.RawConfig)
	}
	return structToRawJSON(v)
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

// marshalToRawJSON converts a raw parsed document (map[string]any, []any,
// json.Number, ...) to a raw JSON value (any) for schema validation.
func marshalToRawJSON(raw any) (any, error) {
	data, err := json.Marshal(raw)
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
	return flattenDetailedSchemaErrors(err, out)
}

func flattenDetailedSchemaErrors(err error, out *jsonschema.OutputUnit) []ValidationIssue {
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
