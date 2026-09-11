package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/beelzebub-labs/beelzebub/v3/specs"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestRunValidateSpecs(t *testing.T) {
	t.Run("valid yaml and ignored entries return success", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "valid.yaml"), "apiVersion: \"v1\"\nprotocol: ssh\naddress: \":22\"\nserverVersion: OpenSSH_9.0\npasswordRegex: ^(.+)$\ncommands:\n  - regex: ^ls$\n    handler: files\n")
		writeTestFile(t, filepath.Join(dir, "notes.txt"), "not yaml\n")
		writeTestFile(t, filepath.Join(dir, "nested", "ignored.yaml"), "protocol: ftp\naddress: \":21\"\n")

		var stdout bytes.Buffer
		var stderr bytes.Buffer

		code := run([]string{"-configs", dir}, &stdout, &stderr)

		assert.Equal(t, 0, code)
		assert.Empty(t, stderr.String())
		assert.Contains(t, stdout.String(), "✓ valid.yaml")
		assert.Contains(t, stdout.String(), "1 files: 1 passed, 0 failed")
		assert.NotContains(t, stdout.String(), "notes.txt")
		assert.NotContains(t, stdout.String(), "ignored.yaml")
	})

	t.Run("mixed failures report output and exit code", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "valid.yaml"), "apiVersion: \"v1\"\nprotocol: ssh\naddress: \":22\"\nserverVersion: OpenSSH_9.0\npasswordRegex: ^(.+)$\ncommands:\n  - regex: ^ls$\n    handler: files\n")
		writeTestFile(t, filepath.Join(dir, "invalid-schema.yaml"), "apiVersion: \"v1\"\nprotocol: ftp\naddress: \":21\"\n")
		writeTestFile(t, filepath.Join(dir, "malformed.yaml"), "protocol: ssh\naddress: \":22\"\ncommands: [\n")
		writeTestFile(t, filepath.Join(dir, "notes.txt"), "not yaml\n")
		writeTestFile(t, filepath.Join(dir, "nested", "ignored.yaml"), "protocol: ftp\naddress: \":21\"\n")

		var stdout bytes.Buffer
		var stderr bytes.Buffer

		code := run([]string{"-configs", dir}, &stdout, &stderr)

		assert.Equal(t, 1, code)
		assert.Empty(t, stderr.String())
		assert.Contains(t, stdout.String(), "✓ valid.yaml")
		assert.Contains(t, stdout.String(), "✗ invalid-schema.yaml")
		assert.Contains(t, stdout.String(), "✗ malformed.yaml")
		assert.Contains(t, stdout.String(), "value must be one of")
		assert.Contains(t, stdout.String(), "parsing YAML:")
		assert.Contains(t, stdout.String(), "3 files: 1 passed, 2 failed")
		assert.NotContains(t, stdout.String(), "notes.txt")
		assert.NotContains(t, stdout.String(), "ignored.yaml")
	})
}

func writeEmbeddedSchemas(t *testing.T, dir string) {
	t.Helper()
	entries, err := specs.FS.ReadDir(".")
	if err != nil {
		t.Fatalf("read embedded specs: %v", err)
	}
	for _, entry := range entries {
		data, err := specs.FS.ReadFile(entry.Name())
		if err != nil {
			t.Fatalf("read embedded schema %s: %v", entry.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dir, entry.Name()), data, 0o644); err != nil {
			t.Fatalf("write schema %s: %v", entry.Name(), err)
		}
	}
}

func TestRunValidateSpecs_SpecsFlag(t *testing.T) {
	t.Run("valid specs dir", func(t *testing.T) {
		configDir := t.TempDir()
		specDir := t.TempDir()
		writeEmbeddedSchemas(t, specDir)
		writeTestFile(t, filepath.Join(configDir, "valid.yaml"), "apiVersion: \"v1\"\nprotocol: ssh\naddress: \":22\"\nserverVersion: OpenSSH_9.0\npasswordRegex: ^(.+)$\ncommands:\n  - regex: ^ls$\n    handler: files\n")

		var stdout bytes.Buffer
		var stderr bytes.Buffer

		code := run([]string{"-configs", configDir, "-specs", specDir}, &stdout, &stderr)

		assert.Equal(t, 0, code)
		assert.Empty(t, stderr.String())
		assert.Contains(t, stdout.String(), "✓ valid.yaml")
	})

	t.Run("missing specs dir", func(t *testing.T) {
		configDir := t.TempDir()
		missing := filepath.Join(t.TempDir(), "does-not-exist")

		var stdout bytes.Buffer
		var stderr bytes.Buffer

		code := run([]string{"-configs", configDir, "-specs", missing}, &stdout, &stderr)

		assert.Equal(t, 1, code)
		assert.Empty(t, stdout.String())
		assert.Contains(t, stderr.String(), "error: loading specs dir:")
		assert.Contains(t, stderr.String(), missing)
	})

	t.Run("specs dir without base schema", func(t *testing.T) {
		configDir := t.TempDir()
		specDir := t.TempDir()

		var stdout bytes.Buffer
		var stderr bytes.Buffer

		code := run([]string{"-configs", configDir, "-specs", specDir}, &stdout, &stderr)

		assert.Equal(t, 1, code)
		assert.Empty(t, stdout.String())
		assert.Contains(t, stderr.String(), "error: loading specs dir:")
	})
}

func TestRunValidateSpecs_RawDocumentValidation(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "unknown top-level property",
			yaml:    "apiVersion: \"v1\"\nprotocol: ssh\naddress: \":22\"\nserverVersion: OpenSSH_9.0\npasswordRegex: ^(.+)$\ncommmands:\n  - regex: ^ls$\n    handler: files\n",
			wantErr: "commmands",
		},
		{
			name:    "nested typo property",
			yaml:    "apiVersion: \"v1\"\nprotocol: ssh\naddress: \":22\"\nserverVersion: OpenSSH_9.0\npasswordRegex: ^(.+)$\ncommands:\n  - regex: ^ls$\n    handlr: files\n",
			wantErr: "handlr",
		},
		{
			name:    "explicit zero value",
			yaml:    "apiVersion: \"v1\"\nprotocol: http\naddress: \":8080\"\ncommands:\n  - regex: .*\n    handler: ok\n    statusCode: 0\n",
			wantErr: "statusCode",
		},
		{
			name:    "explicit empty collection",
			yaml:    "apiVersion: \"v1\"\nprotocol: http\naddress: \":8080\"\ncommands: []\n",
			wantErr: "commands",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTestFile(t, filepath.Join(dir, "svc.yaml"), tt.yaml)

			var stdout bytes.Buffer
			var stderr bytes.Buffer

			code := run([]string{"-configs", dir}, &stdout, &stderr)

			assert.Equal(t, 1, code)
			assert.Empty(t, stderr.String())
			assert.Contains(t, stdout.String(), "✗ svc.yaml")
			assert.Contains(t, stdout.String(), tt.wantErr)
		})
	}
}

func TestRunValidateSpecs_PathAndReadErrors(t *testing.T) {
	oldAbs := resolveAbsolutePath
	oldRead := readConfigFile
	t.Cleanup(func() {
		resolveAbsolutePath = oldAbs
		readConfigFile = oldRead
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	resolveAbsolutePath = func(string) (string, error) { return "", os.ErrInvalid }
	assert.Equal(t, 1, run([]string{"-configs", "configs"}, &stdout, &stderr))
	assert.Contains(t, stderr.String(), "resolving configs path")

	configDir := t.TempDir()
	writeTestFile(t, filepath.Join(configDir, "unreadable.yaml"), "protocol: ssh\n")
	stdout.Reset()
	stderr.Reset()
	resolveAbsolutePath = oldAbs
	readConfigFile = func(string) ([]byte, error) { return nil, os.ErrPermission }
	assert.Equal(t, 1, run([]string{"-configs", configDir}, &stdout, &stderr))
	assert.Contains(t, stdout.String(), "reading file")
}

func TestRunValidateSpecs_MissingDirectory(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "does-not-exist")

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run([]string{"-configs", missingDir}, &stdout, &stderr)

	assert.Equal(t, 1, code)
	assert.Empty(t, stdout.String())
	assert.True(t, strings.Contains(stderr.String(), "error: reading configs dir:"))
	assert.Contains(t, stderr.String(), missingDir)
}

func TestRunValidateSpecs_FlagErrors(t *testing.T) {
	t.Run("help", func(t *testing.T) {
		var stdout bytes.Buffer
		var stderr bytes.Buffer

		code := run([]string{"-h"}, &stdout, &stderr)

		assert.Equal(t, 0, code)
		assert.Empty(t, stdout.String())
		assert.Contains(t, stderr.String(), "Usage of "+os.Args[0]+":")
		assert.Contains(t, stderr.String(), "-configs")
	})

	t.Run("invalid flag", func(t *testing.T) {
		var stdout bytes.Buffer
		var stderr bytes.Buffer

		code := run([]string{"-unknown"}, &stdout, &stderr)

		assert.Equal(t, 2, code)
		assert.Empty(t, stdout.String())
		assert.Contains(t, stderr.String(), "flag provided but not defined: -unknown")
	})
}

func TestMain_UsesExitHook(t *testing.T) {
	oldArgs := os.Args
	oldExit := exitProcess
	t.Cleanup(func() {
		os.Args = oldArgs
		exitProcess = oldExit
	})

	exitCode := -1
	exitProcess = func(code int) { exitCode = code }
	os.Args = []string{"validate-specs", "-h"}

	main()

	if exitCode != 0 {
		t.Fatalf("main exit code = %d, want 0", exitCode)
	}
}
