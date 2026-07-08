package pluginmgr

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeDeclared(t *testing.T, m *Manager, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(m.declaredPath()), 0o755))
	require.NoError(t, os.WriteFile(m.declaredPath(), []byte(content), 0o644))
}

func TestDeclaredSources_ParsesYAML(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)

	writeDeclared(t, m, `
plugins:
  - source: github.com/acme/scanner
  - source: " github.com/acme/other "
`)

	got, err := m.declaredSources()
	require.NoError(t, err)
	assert.Equal(t, []string{"github.com/acme/scanner", "github.com/acme/other"}, got)
}

func TestDeclaredSources_MissingFile(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	got, err := m.declaredSources()
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestDeclaredSources_EmptyFile(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	writeDeclared(t, m, "")

	got, err := m.declaredSources()
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestDeclaredSources_RejectsUnknownFieldsAndMissingSource(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	writeDeclared(t, m, "plugins:\n  - src: github.com/acme/scanner\n")

	_, err := m.declaredSources()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field src not found")

	writeDeclared(t, m, "plugins:\n  - source: ''\n")
	_, err = m.declaredSources()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source is required")
}

func TestDeclare_AppendsAndDedupes(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)

	require.NoError(t, m.Declare("github.com/acme/scanner"))
	require.NoError(t, m.Declare("github.com/acme/scanner")) // duplicate, ignored
	require.NoError(t, m.Declare("github.com/acme/other"))

	got, err := m.declaredSources()
	require.NoError(t, err)
	assert.Equal(t, []string{"github.com/acme/scanner", "github.com/acme/other"}, got)

	raw, err := os.ReadFile(m.declaredPath())
	require.NoError(t, err)
	assert.Contains(t, string(raw), "Plugins to compile into this deployment.")
}

func TestDeclare_RejectsEmptySource(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	err := m.Declare(" ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plugin source is required")
}

func TestUndeclare_RemovesMatchingSourceAndPreservesComments(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)

	writeDeclared(t, m, `
plugins:
  - source: github.com/acme/scanner
  - source: github.com/acme/other
`)

	err := m.Undeclare(LockedPlugin{
		Name:     "scanner",
		Source:   "https://github.com/acme/scanner.git",
		Declared: "github.com/acme/scanner",
	})

	require.NoError(t, err)
	raw, err := os.ReadFile(m.declaredPath())
	require.NoError(t, err)

	assert.Contains(t, string(raw), "source: github.com/acme/other")
	assert.NotContains(t, string(raw), "source: github.com/acme/scanner")
}

func TestRemove_UndeclaresPlugin(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	ctx := context.Background()
	writeDeclared(t, m, "plugins:\n  - source: github.com/acme/scanner\n")

	_, err := m.InstallDeclared(ctx, false)
	require.NoError(t, err)
	_, err = m.Remove(ctx, "scanner")
	require.NoError(t, err)

	got, err := m.declaredSources()
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestInstallDeclared_InstallsThenIsIdempotent(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	writeDeclared(t, m, "plugins:\n  - source: github.com/acme/scanner\n")
	ctx := context.Background()

	// First run installs the declared plugin.
	installed, err := m.InstallDeclared(ctx, false)
	require.NoError(t, err)
	require.Len(t, installed, 1)
	assert.Equal(t, "scanner", installed[0].Name)
	assert.DirExists(t, filepath.Join(m.moduleRoot, "plugins", "scanner"))

	// Second run is a no-op: already installed, skipped without re-cloning.
	installed, err = m.InstallDeclared(ctx, false)
	require.NoError(t, err)
	assert.Empty(t, installed)
}

func TestInstallDeclared_EmptyFile(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	writeDeclared(t, m, "plugins: []\n")

	installed, err := m.InstallDeclared(context.Background(), false)
	require.NoError(t, err)
	assert.Empty(t, installed)
}

func TestInstallDeclared_ReinstallsOnRefChange(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	ctx := context.Background()

	writeDeclared(t, m, "plugins:\n  - source: github.com/acme/scanner@v1.0.0\n")
	installed, err := m.InstallDeclared(ctx, false)
	require.NoError(t, err)
	require.Len(t, installed, 1)
	assert.Equal(t, "v1.0.0", installed[0].Ref)

	// Same ref → skipped.
	installed, err = m.InstallDeclared(ctx, false)
	require.NoError(t, err)
	assert.Empty(t, installed)

	// Bumped ref → re-installed at the new ref.
	writeDeclared(t, m, "plugins:\n  - source: github.com/acme/scanner@v2.0.0\n")
	installed, err = m.InstallDeclared(ctx, false)
	require.NoError(t, err)
	require.Len(t, installed, 1)
	assert.Equal(t, "v2.0.0", installed[0].Ref)
}

func TestUpdate_AllAndByName(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	ctx := context.Background()
	writeDeclared(t, m, "plugins:\n  - source: github.com/acme/scanner@main\n")
	_, err := m.InstallDeclared(ctx, false)
	require.NoError(t, err)

	// Update all.
	results, err := m.Update(ctx, "")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "scanner", results[0].Name)
	assert.False(t, results[0].Changed) // fake returns the same commit

	// Update by name.
	results, err = m.Update(ctx, "scanner")
	require.NoError(t, err)
	require.Len(t, results, 1)
}

func TestUpdate_UnknownName(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	ctx := context.Background()
	writeDeclared(t, m, "plugins:\n  - source: github.com/acme/scanner\n")
	_, err := m.InstallDeclared(ctx, false)
	require.NoError(t, err)

	_, err = m.Update(ctx, "ghost")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not installed")
}
