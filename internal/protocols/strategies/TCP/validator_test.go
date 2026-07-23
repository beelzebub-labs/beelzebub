package TCP

import (
	"os"
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/stretchr/testify/assert"
)

func existingFile() string {
	exe, err := os.Executable()
	if err != nil {
		return "/tmp"
	}
	return exe
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
	existing := existingFile()
	config := parser.BeelzebubServiceConfiguration{
		Protocol:    "tcp",
		TLSCertPath: existing,
		TLSKeyPath:  existing,
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
	existing := existingFile()
	config := parser.BeelzebubServiceConfiguration{
		Protocol:    "tcp",
		TLSCertPath: existing,
		TLSKeyPath:  existing,
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
	existing := existingFile()
	config := parser.BeelzebubServiceConfiguration{
		Protocol:    "tcp",
		TLSCertPath: existing,
		TLSKeyPath:  "/nonexistent/cert.key",
	}
	issues := v.Validate(config)
	assert.Len(t, issues, 1)
	assert.Equal(t, parser.LevelWarning, issues[0].Level)
	assert.Contains(t, issues[0].Message, "tlsKeyPath file does not exist")
}
