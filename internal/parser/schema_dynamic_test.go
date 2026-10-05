package parser

import "testing"

func TestValidateConfigSchema_DynamicKey(t *testing.T) {
	ResetSchemaCache()
	defer ResetSchemaCache()
	config := BeelzebubServiceConfiguration{
		ApiVersion: "v1", Protocol: "http", Address: ":8080",
		Commands: []Command{{RegexStr: ".*", Handler: "{{uuid.v4}}", StatusCode: 200}},
		Dynamic:  &DynamicResponses{Enabled: true},
	}
	v := &SchemaValidator{}
	if issues := v.Validate(config); len(issues) != 0 {
		t.Fatalf("expected no schema issues, got %+v", issues)
	}
}
