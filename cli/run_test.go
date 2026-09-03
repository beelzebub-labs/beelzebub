package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunBeelzebub_InvalidCoreYaml(t *testing.T) {
	tmpDir := t.TempDir()
	corePath := filepath.Join(tmpDir, "core.yaml")
	os.WriteFile(corePath, []byte("invalid: yaml: :"), 0644)

	rootConfCore = corePath
	rootConfServices = tmpDir
	runMemLimitMiB = -1 // Disable memory limit for test

	err := runBeelzebub(runCmd, nil)
	if err == nil {
		t.Fatal("expected error with invalid core yaml, got nil")
	}

	if !strings.Contains(err.Error(), "reading core config:") {
		t.Errorf("expected error to mention core config reading, got: %v", err)
	}
}

func TestValidateRuntimeConfigurationRejectsSchemaErrors(t *testing.T) {
	core := &parser.BeelzebubCoreConfigurations{}
	services := []parser.BeelzebubServiceConfiguration{{Filename: "invalid.yaml", Protocol: "unknown", Address: ":1"}}
	if err := validateRuntimeConfiguration(core, services); err == nil {
		t.Fatal("expected runtime validation error")
	}
}

func TestValidateRuntimeConfigurationAcceptsValidService(t *testing.T) {
	core := &parser.BeelzebubCoreConfigurations{}
	services := []parser.BeelzebubServiceConfiguration{{
		Filename: "valid.yaml", ApiVersion: "v1", Protocol: "http", Address: ":8080",
		Commands: []parser.Command{{RegexStr: ".*", Handler: "ok"}},
	}}
	if err := validateRuntimeConfiguration(core, services); err != nil {
		t.Fatalf("expected valid runtime configuration, got %v", err)
	}
}

func TestRunBeelzebub_InvalidServicesYaml(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "svc.yaml"), []byte("invalid: yaml: :"), 0644)

	rootConfCore = "../configurations/beelzebub.yaml"
	rootConfServices = tmpDir
	runMemLimitMiB = 100 // Test memory limit path

	err := runBeelzebub(runCmd, nil)
	if err == nil {
		t.Fatal("expected error with invalid services yaml, got nil")
	}

	if !strings.Contains(err.Error(), "reading services config:") {
		t.Errorf("expected error to mention services config reading, got: %v", err)
	}
}

func TestRunBeelzebub_NoServicesConfigured(t *testing.T) {
	tmpDir := t.TempDir() // empty directory, no services

	rootConfCore = "../configurations/beelzebub.yaml"
	rootConfServices = tmpDir
	runMemLimitMiB = -1

	// Ensure BEELZEBUB_CLOUD_ENABLED is false to trigger the "no services configured" error
	os.Setenv("BEELZEBUB_CLOUD_ENABLED", "false")
	defer os.Unsetenv("BEELZEBUB_CLOUD_ENABLED")

	err := runBeelzebub(runCmd, nil)
	if err == nil {
		t.Fatal("expected error for no services configured, got nil")
	}

	if !strings.Contains(err.Error(), "no services configured") {
		t.Errorf("expected error to mention no services configured, got: %v", err)
	}
}

func TestRunBeelzebub_RejectsInvalidRuntimeService(t *testing.T) {
	tmpDir := t.TempDir()
	corePath := filepath.Join(tmpDir, "core.yaml")
	require.NoError(t, os.WriteFile(corePath, []byte("core:\n  beelzebub-cloud:\n    enabled: false\n"), 0o644))
	servicesDir := filepath.Join(tmpDir, "services")
	require.NoError(t, os.MkdirAll(servicesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(servicesDir, "invalid.yaml"), []byte("apiVersion: v1\nprotocol: unknown\naddress: ':1'\n"), 0o644))

	oldCore, oldServices, oldLimit := rootConfCore, rootConfServices, runMemLimitMiB
	t.Cleanup(func() {
		rootConfCore, rootConfServices, runMemLimitMiB = oldCore, oldServices, oldLimit
	})
	rootConfCore, rootConfServices, runMemLimitMiB = corePath, servicesDir, -1

	err := runBeelzebub(runCmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runtime configuration validation failed")
}

func TestListPlugins_NoPanic(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	listPlugins(cmd, nil)
}

func TestPrintVersion_NoPanic(t *testing.T) {
	// Just verify the function doesn't panic
	printVersion(nil, nil)
}

func TestPrintVersion_WithBuildInfo(t *testing.T) {
	Version = "dev"
	CommitSHA = "unknown"

	// Should not panic even without ldflags
	printVersion(nil, nil)

	// Verify version is set (may be from build info or default)
	assert.Equal(t, "dev", Version)
}

func TestRunBeelzebub_Success(t *testing.T) {
	tmpDir := t.TempDir()

	// Write minimal core config
	corePath := filepath.Join(tmpDir, "core.yaml")
	os.WriteFile(corePath, []byte(`core:
  logging:
    debug: false
    debugReportCaller: false
    logDisableTimestamp: true
  beelzebub-cloud:
    enabled: false
`), 0644)

	// Write a minimal service config with a dynamic port
	svcDir := filepath.Join(tmpDir, "services")
	os.Mkdir(svcDir, 0755)
	os.WriteFile(filepath.Join(svcDir, "svc.yaml"), []byte(`apiVersion: "v1"
protocol: "http"
address: "127.0.0.1:0"
description: "test"
commands:
  - regex: ".*"
    handler: "ok"
`), 0644)

	rootConfCore = corePath
	rootConfServices = svcDir
	runMemLimitMiB = -1

	// Inject a pre-filled signal channel so runBeelzebub returns immediately
	// after starting services and reaching the signal wait.
	sigCh := make(chan os.Signal, 1)
	sigCh <- syscall.SIGTERM
	testShutdownCh = sigCh
	defer func() { testShutdownCh = nil }()

	err := runBeelzebub(runCmd, nil)
	assert.NoError(t, err)
}

func TestListPlugins_Empty(t *testing.T) {
	plugin.Cleanup()

	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	listPlugins(cmd, nil)
	require.Empty(t, plugin.List())
}
