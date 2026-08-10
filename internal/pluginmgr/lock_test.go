package pluginmgr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLockFile_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), LockFileName)

	lf := &LockFile{}
	lf.Upsert(LockedPlugin{Name: "zeta", Module: "github.com/acme/zeta", Version: "1.0.0", Import: "github.com/acme/zeta", Dir: "plugins/zeta", Commit: "abc"})
	lf.Upsert(LockedPlugin{Name: "alpha", Module: "github.com/acme/alpha", Version: "2.0.0", Import: "github.com/acme/alpha", Dir: "plugins/alpha", Commit: "def"})
	require.NoError(t, lf.Save(path))

	got, err := LoadLockFile(path)
	require.NoError(t, err)
	require.Len(t, got.Plugins, 2)
	// Saved sorted by name.
	assert.Equal(t, "alpha", got.Plugins[0].Name)
	assert.Equal(t, "zeta", got.Plugins[1].Name)
}

func TestLoadLockFile_Missing(t *testing.T) {
	lf, err := LoadLockFile(filepath.Join(t.TempDir(), "nope.yaml"))
	require.NoError(t, err)
	assert.Empty(t, lf.Plugins)
}

func TestLoadLockFile_InvalidYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), LockFileName)
	require.NoError(t, os.WriteFile(path, []byte("plugins:\n  - name: [bad\n"), 0o644))

	_, err := LoadLockFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing")
}

func TestLockFile_CloneNilAndCopy(t *testing.T) {
	var nilLock *LockFile
	assert.Empty(t, nilLock.Clone().Plugins)

	lf := &LockFile{Plugins: []LockedPlugin{{Name: "scanner"}}}
	clone := lf.Clone()
	clone.Plugins[0].Name = "changed"

	assert.Equal(t, "scanner", lf.Plugins[0].Name)
	assert.Equal(t, "changed", clone.Plugins[0].Name)
}

func TestLockFile_UpsertReplaces(t *testing.T) {
	lf := &LockFile{}
	lf.Upsert(LockedPlugin{Name: "scanner", Version: "1.0.0"})
	lf.Upsert(LockedPlugin{Name: "scanner", Version: "2.0.0"})
	require.Len(t, lf.Plugins, 1)
	assert.Equal(t, "2.0.0", lf.Plugins[0].Version)
}

func TestLockFile_FindRemove(t *testing.T) {
	lf := &LockFile{}
	lf.Upsert(LockedPlugin{Name: "scanner"})

	_, ok := lf.Find("scanner")
	assert.True(t, ok)
	_, ok = lf.Find("ghost")
	assert.False(t, ok)

	assert.True(t, lf.Remove("scanner"))
	assert.False(t, lf.Remove("scanner"))
	assert.Empty(t, lf.Plugins)
}
