package TCP

import (
	"os"
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/stretchr/testify/assert"
)

func existingTLSFile(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/tls.pem"
	if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
		t.Fatalf("create TLS test file: %v", err)
	}
	return path
}

func TestTCPValidator_Name(t *testing.T) {
	v := &TCPValidator{}
	assert.Equal(t, "tcp", v.Name())
}

func TestTCPValidator_NotTCPProtocol(t *testing.T) {
	v := &TCPValidator{}
	config := parser.BeelzebubServiceConfiguration{
		Protocol: "http",
	}
	issues := v.Validate(config)
	assert.Empty(t, issues)
}

func TestTCPValidator_BothTLSSet(t *testing.T) {
	v := &TCPValidator{}
	path := existingTLSFile(t)
	config := parser.BeelzebubServiceConfiguration{
		Protocol:    "tcp",
		TLSCertPath: path,
		TLSKeyPath:  path,
	}
	issues := v.Validate(config)
	assert.Empty(t, issues)
}

func TestTCPValidator_NoTLSSet(t *testing.T) {
	v := &TCPValidator{}
	config := parser.BeelzebubServiceConfiguration{
		Protocol: "tcp",
	}
	issues := v.Validate(config)
	assert.Empty(t, issues)
}

func TestTCPValidator_WirePluginRegistry(t *testing.T) {
	v := &TCPValidator{}

	issues := v.Validate(parser.BeelzebubServiceConfiguration{Protocol: "tcp", WirePlugins: []string{"vnc"}})
	assert.Empty(t, issues)

	issues = v.Validate(parser.BeelzebubServiceConfiguration{Protocol: "tcp", WirePlugins: []string{"missing-wire-plugin"}})
	assert.Len(t, issues, 1)
	assert.Equal(t, parser.LevelError, issues[0].Level)
	assert.Contains(t, issues[0].Message, "not registered")

	issues = v.Validate(parser.BeelzebubServiceConfiguration{Protocol: "tcp", WirePlugins: []string{"vnc", "vnc"}})
	assert.Len(t, issues, 1)
	assert.Contains(t, issues[0].Message, "declared more than once")
}

func TestTCPValidator_OnlyCert(t *testing.T) {
	v := &TCPValidator{}
	config := parser.BeelzebubServiceConfiguration{
		Protocol:    "tcp",
		TLSCertPath: "/tmp/cert.crt",
		TLSKeyPath:  "",
	}
	issues := v.Validate(config)
	assert.Len(t, issues, 1)
	assert.Equal(t, parser.LevelError, issues[0].Level)
	assert.Equal(t, "both tlsCertPath and tlsKeyPath must be set for TLS, or neither", issues[0].Message)
}

func TestTCPValidator_OnlyKey(t *testing.T) {
	v := &TCPValidator{}
	config := parser.BeelzebubServiceConfiguration{
		Protocol:    "tcp",
		TLSCertPath: "",
		TLSKeyPath:  "/tmp/cert.key",
	}
	issues := v.Validate(config)
	assert.Len(t, issues, 1)
	assert.Equal(t, parser.LevelError, issues[0].Level)
	assert.Equal(t, "both tlsCertPath and tlsKeyPath must be set for TLS, or neither", issues[0].Message)
}

func TestTCPValidator_TLSFilesExist(t *testing.T) {
	v := &TCPValidator{}
	path := existingTLSFile(t)
	config := parser.BeelzebubServiceConfiguration{
		Protocol:    "tcp",
		TLSCertPath: path,
		TLSKeyPath:  path,
	}
	issues := v.Validate(config)
	assert.Empty(t, issues)
}

func TestTCPValidator_TLSFilesNotExist(t *testing.T) {
	v := &TCPValidator{}
	config := parser.BeelzebubServiceConfiguration{
		Protocol:    "tcp",
		TLSCertPath: "/nonexistent/cert.crt",
		TLSKeyPath:  "/nonexistent/cert.key",
	}
	issues := v.Validate(config)
	assert.Len(t, issues, 2)
	for _, issue := range issues {
		assert.Equal(t, parser.LevelWarning, issue.Level)
		assert.Contains(t, issue.Message, "does not exist")
	}
}

func TestTCPValidator_TLSOneFileNotExist(t *testing.T) {
	v := &TCPValidator{}
	path := existingTLSFile(t)
	config := parser.BeelzebubServiceConfiguration{
		Protocol:    "tcp",
		TLSCertPath: path,
		TLSKeyPath:  "/nonexistent/cert.key",
	}
	issues := v.Validate(config)
	assert.Len(t, issues, 1)
	assert.Equal(t, parser.LevelWarning, issues[0].Level)
	assert.Contains(t, issues[0].Message, "tlsKeyPath file does not exist")
}
