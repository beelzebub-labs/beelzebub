package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
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
