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
	require.NoError(t, os.WriteFile(m.declaredPath(), []byte(content), 0o644))
}

func TestDeclaredSources_ParsesAndSkipsComments(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	writeDeclared(t, m, "# a comment\n\ngithub.com/acme/scanner\n  github.com/acme/other  \n")

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

func TestDeclare_AppendsAndDedupes(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)

	require.NoError(t, m.Declare("github.com/acme/scanner"))
	require.NoError(t, m.Declare("github.com/acme/scanner")) // duplicate, ignored
	require.NoError(t, m.Declare("github.com/acme/other"))

	got, err := m.declaredSources()
	require.NoError(t, err)
	assert.Equal(t, []string{"github.com/acme/scanner", "github.com/acme/other"}, got)
}

func TestInstallDeclared_InstallsThenIsIdempotent(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	writeDeclared(t, m, "github.com/acme/scanner\n")
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
	writeDeclared(t, m, "# nothing here\n")

	installed, err := m.InstallDeclared(context.Background(), false)
	require.NoError(t, err)
	assert.Empty(t, installed)
}

func TestInstallDeclared_ReinstallsOnRefChange(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	ctx := context.Background()

	writeDeclared(t, m, "github.com/acme/scanner@v1.0.0\n")
	installed, err := m.InstallDeclared(ctx, false)
	require.NoError(t, err)
	require.Len(t, installed, 1)
	assert.Equal(t, "v1.0.0", installed[0].Ref)

	// Same ref → skipped.
	installed, err = m.InstallDeclared(ctx, false)
	require.NoError(t, err)
	assert.Empty(t, installed)

	// Bumped ref → re-installed at the new ref.
	writeDeclared(t, m, "github.com/acme/scanner@v2.0.0\n")
	installed, err = m.InstallDeclared(ctx, false)
	require.NoError(t, err)
	require.Len(t, installed, 1)
	assert.Equal(t, "v2.0.0", installed[0].Ref)
}

func TestUpdate_AllAndByName(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	ctx := context.Background()
	writeDeclared(t, m, "github.com/acme/scanner@main\n")
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
	writeDeclared(t, m, "github.com/acme/scanner\n")
	_, err := m.InstallDeclared(ctx, false)
	require.NoError(t, err)

	_, err = m.Update(ctx, "ghost")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not installed")
}
