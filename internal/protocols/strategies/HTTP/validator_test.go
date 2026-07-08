package HTTP

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
	path := existingTLSFile(t)
	config := parser.BeelzebubServiceConfiguration{
		Protocol:    "http",
		TLSCertPath: path,
		TLSKeyPath:  path,
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
	path := existingTLSFile(t)
	config := parser.BeelzebubServiceConfiguration{
		Protocol:    "http",
		TLSCertPath: path,
		TLSKeyPath:  path,
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
	path := existingTLSFile(t)
	config := parser.BeelzebubServiceConfiguration{
		Protocol:    "http",
		TLSCertPath: path,
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
		Protocol:        "http",
		FallbackCommand: parser.Command{Handler: "fallback"},
	}
	issues := v.Validate(config)
	for _, issue := range issues {
		assert.NotContains(t, issue.Message, "no fallbackCommand")
	}
}

func TestHTTPValidator_CatchAllCommandNeedsNoFallback(t *testing.T) {
	v := &HTTPValidator{}
	for _, regex := range []string{".*", "^.*$", "^(.*)$"} {
		issues := v.Validate(parser.BeelzebubServiceConfiguration{Protocol: "http", Commands: []parser.Command{{RegexStr: regex, Handler: "ok"}}})
		for _, issue := range issues {
			assert.NotContains(t, issue.Message, "no fallbackCommand")
		}
	}
}

func TestHTTPValidator_PartialCommandsNeedFallback(t *testing.T) {
	issues := (&HTTPValidator{}).Validate(parser.BeelzebubServiceConfiguration{Protocol: "http", Commands: []parser.Command{{RegexStr: "^GET /api", Handler: "ok"}}})
	assert.Contains(t, issues, parser.ValidationIssue{Level: parser.LevelWarning, Message: "HTTP service has commands but no fallbackCommand — unmatched requests will return empty 200 OK"})
}

func TestHTTPValidator_InvalidRegexIsNotCatchAll(t *testing.T) {
	issues := (&HTTPValidator{}).Validate(parser.BeelzebubServiceConfiguration{Protocol: "http", Commands: []parser.Command{{RegexStr: "^(?:.*)$", Handler: "ok"}}})
	assert.Contains(t, issues, parser.ValidationIssue{Level: parser.LevelWarning, Message: "HTTP service has commands but no fallbackCommand — unmatched requests will return empty 200 OK"})
}
