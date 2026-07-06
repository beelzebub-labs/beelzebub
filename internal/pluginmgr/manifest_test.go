package pluginmgr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeManifest(t *testing.T, dir, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ManifestFile), []byte(content), 0o644))
}

func TestLoadManifest(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `
name: scanner
version: 1.2.0
module: github.com/acme/scanner
entrypoint: .
author: acme
`)
	m, err := LoadManifest(dir)
	require.NoError(t, err)
	assert.Equal(t, "scanner", m.Name)
	assert.Equal(t, "1.2.0", m.Version)
	assert.Equal(t, "github.com/acme/scanner", m.Module)
}

func TestLoadManifest_Missing(t *testing.T) {
	_, err := LoadManifest(t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
}

func TestManifestValidate(t *testing.T) {
	base := Manifest{Name: "scanner", Version: "1.2.0", Module: "github.com/acme/scanner"}

	tests := []struct {
		name        string
		mutate      func(*Manifest)
		goMod       string
		coreVersion string
		wantErr     string
	}{
		{name: "valid", mutate: func(*Manifest) {}, goMod: "github.com/acme/scanner"},
		{name: "missing name", mutate: func(m *Manifest) { m.Name = "" }, goMod: "github.com/acme/scanner", wantErr: "name is required"},
		{name: "bad name", mutate: func(m *Manifest) { m.Name = "Scanner!" }, goMod: "github.com/acme/scanner", wantErr: "is invalid"},
		{name: "missing version", mutate: func(m *Manifest) { m.Version = "" }, goMod: "github.com/acme/scanner", wantErr: "version is required"},
		{name: "bad version", mutate: func(m *Manifest) { m.Version = "1.2" }, goMod: "github.com/acme/scanner", wantErr: "not semver"},
		{name: "missing module", mutate: func(m *Manifest) { m.Module = "" }, goMod: "github.com/acme/scanner", wantErr: "module is required"},
		{name: "module mismatch", mutate: func(*Manifest) {}, goMod: "github.com/acme/other", wantErr: "does not match go.mod"},
		{
			name:        "min-core too new",
			mutate:      func(m *Manifest) { m.MinCoreVersion = "v3.5.0" },
			goMod:       "github.com/acme/scanner",
			coreVersion: "v3.4.0",
			wantErr:     "requires core",
		},
		{
			name:        "min-core satisfied",
			mutate:      func(m *Manifest) { m.MinCoreVersion = "v3.4.0" },
			goMod:       "github.com/acme/scanner",
			coreVersion: "v3.4.0",
		},
		{
			name:        "min-core skipped on dev core",
			mutate:      func(m *Manifest) { m.MinCoreVersion = "v9.9.9" },
			goMod:       "github.com/acme/scanner",
			coreVersion: "dev",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := base
			tt.mutate(&m)
			err := m.Validate(tt.goMod, tt.coreVersion)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestManifestImportPath(t *testing.T) {
	assert.Equal(t, "github.com/acme/scanner",
		Manifest{Module: "github.com/acme/scanner", Entrypoint: "."}.ImportPath())
	assert.Equal(t, "github.com/acme/scanner",
		Manifest{Module: "github.com/acme/scanner", Entrypoint: ""}.ImportPath())
	assert.Equal(t, "github.com/acme/scanner/plugin",
		Manifest{Module: "github.com/acme/scanner", Entrypoint: "plugin"}.ImportPath())
	assert.Equal(t, "github.com/acme/scanner/cmd/plugin",
		Manifest{Module: "github.com/acme/scanner", Entrypoint: "./cmd/plugin"}.ImportPath())
}

func TestCompareVersions(t *testing.T) {
	assert.Equal(t, -1, compareVersions("v3.4.0", "v3.5.0"))
	assert.Equal(t, 1, compareVersions("v3.5.0", "v3.4.0"))
	assert.Equal(t, 0, compareVersions("v3.4.0", "3.4.0"))
	assert.Equal(t, 1, compareVersions("v3.4.1", "v3.4.0"))
	assert.Equal(t, -1, compareVersions("v3.4.0-rc1", "v3.4.1"))
}
