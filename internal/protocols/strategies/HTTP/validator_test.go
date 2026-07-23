package HTTP

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

func TestHTTPValidator_Name(t *testing.T) {
	v := &HTTPValidator{}
	assert.Equal(t, "http", v.Name())
}

func TestHTTPValidator_NotHTTPProtocol(t *testing.T) {
	v := &HTTPValidator{}
	config := parser.BeelzebubServiceConfiguration{
		Protocol: "ssh",
	}
	issues := v.Validate(config)
	assert.Empty(t, issues)
}

func TestHTTPValidator_BothTLSSet(t *testing.T) {
	v := &HTTPValidator{}
	existing := existingFile()
	config := parser.BeelzebubServiceConfiguration{
		Protocol:    "http",
		TLSCertPath: existing,
		TLSKeyPath:  existing,
	}
	issues := v.Validate(config)
	assert.Empty(t, issues)
}

func TestHTTPValidator_NoTLSSet(t *testing.T) {
	v := &HTTPValidator{}
	config := parser.BeelzebubServiceConfiguration{
		Protocol: "http",
	}
	issues := v.Validate(config)
	assert.Empty(t, issues)
}

func TestHTTPValidator_OnlyCert(t *testing.T) {
	v := &HTTPValidator{}
	config := parser.BeelzebubServiceConfiguration{
		Protocol:    "http",
		TLSCertPath: "/tmp/cert.crt",
		TLSKeyPath:  "",
	}
	issues := v.Validate(config)
	assert.Len(t, issues, 1)
	assert.Equal(t, parser.LevelError, issues[0].Level)
	assert.Equal(t, "both tlsCertPath and tlsKeyPath must be set for TLS, or neither", issues[0].Message)
}

func TestHTTPValidator_OnlyKey(t *testing.T) {
	v := &HTTPValidator{}
	config := parser.BeelzebubServiceConfiguration{
		Protocol:    "http",
		TLSCertPath: "",
		TLSKeyPath:  "/tmp/cert.key",
	}
	issues := v.Validate(config)
	assert.Len(t, issues, 1)
	assert.Equal(t, parser.LevelError, issues[0].Level)
	assert.Equal(t, "both tlsCertPath and tlsKeyPath must be set for TLS, or neither", issues[0].Message)
}

func TestHTTPValidator_TLSFilesExist(t *testing.T) {
	v := &HTTPValidator{}
	existing := existingFile()
	config := parser.BeelzebubServiceConfiguration{
		Protocol:    "http",
		TLSCertPath: existing,
		TLSKeyPath:  existing,
	}
	issues := v.Validate(config)
	assert.Empty(t, issues)
}

func TestHTTPValidator_TLSFileNotExist(t *testing.T) {
	v := &HTTPValidator{}
	config := parser.BeelzebubServiceConfiguration{
		Protocol:    "http",
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

func TestHTTPValidator_TLSOneFileNotExist(t *testing.T) {
	v := &HTTPValidator{}
	existing := existingFile()
	config := parser.BeelzebubServiceConfiguration{
		Protocol:    "http",
		TLSCertPath: existing,
		TLSKeyPath:  "/nonexistent/cert.key",
	}
	issues := v.Validate(config)
	assert.Len(t, issues, 1)
	assert.Equal(t, parser.LevelWarning, issues[0].Level)
	assert.Contains(t, issues[0].Message, "tlsKeyPath file does not exist")
}

func TestHTTPValidator_HasCommandsWithFallback(t *testing.T) {
	v := &HTTPValidator{}
	config := parser.BeelzebubServiceConfiguration{
		Protocol: "http",
		Commands: []parser.Command{
			{RegexStr: "^GET /api", Handler: "handler"},
		},
		FallbackCommand: parser.Command{Handler: "fallback"},
	}
	issues := v.Validate(config)
	for _, issue := range issues {
		assert.NotContains(t, issue.Message, "no fallbackCommand")
	}
}

func TestHTTPValidator_HasCommandsWithoutFallback(t *testing.T) {
	v := &HTTPValidator{}
	config := parser.BeelzebubServiceConfiguration{
		Protocol: "http",
		Commands: []parser.Command{
			{RegexStr: "^GET /api", Handler: "handler"},
		},
	}
	issues := v.Validate(config)
	var found bool
	for _, issue := range issues {
		if issue.Message == "HTTP service has commands but no fallbackCommand — unmatched requests will return empty 200 OK" {
			found = true
			assert.Equal(t, parser.LevelWarning, issue.Level)
		}
	}
	assert.True(t, found)
}

func TestHTTPValidator_NoCommandsNoFallback(t *testing.T) {
	v := &HTTPValidator{}
	config := parser.BeelzebubServiceConfiguration{
		Protocol: "http",
	}
	issues := v.Validate(config)
	for _, issue := range issues {
		assert.NotContains(t, issue.Message, "no fallbackCommand")
	}
}

func TestHTTPValidator_NoCommandsWithFallback(t *testing.T) {
	v := &HTTPValidator{}
	config := parser.BeelzebubServiceConfiguration{
		Protocol: "http",
		FallbackCommand: parser.Command{Handler: "fallback"},
	}
	issues := v.Validate(config)
	for _, issue := range issues {
		assert.NotContains(t, issue.Message, "no fallbackCommand")
	}
}
