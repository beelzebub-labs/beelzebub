package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
		writeTestFile(t, filepath.Join(dir, "valid.yaml"), "protocol: ssh\naddress: \":22\"\nserverVersion: OpenSSH_9.0\npasswordRegex: ^(.+)$\ncommands:\n  - regex: ^ls$\n    handler: files\n")
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
		writeTestFile(t, filepath.Join(dir, "valid.yaml"), "protocol: ssh\naddress: \":22\"\nserverVersion: OpenSSH_9.0\npasswordRegex: ^(.+)$\ncommands:\n  - regex: ^ls$\n    handler: files\n")
		writeTestFile(t, filepath.Join(dir, "invalid-schema.yaml"), "protocol: ftp\naddress: \":21\"\n")
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
