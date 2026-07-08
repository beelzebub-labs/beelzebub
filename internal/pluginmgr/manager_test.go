package pluginmgr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeManager(t *testing.T, module, manifest string) (*Manager, *fakeCalls) {
	t.Helper()
	root := t.TempDir()
	calls := &fakeCalls{}
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module github.com/beelzebub-labs/beelzebub/v3\n\ngo 1.25\n"), 0o644))

	run := func(_ context.Context, dir, name string, args ...string) (string, error) {
		switch {
		case name == "git" && len(args) > 0 && args[0] == "clone":
			dst := args[len(args)-1]
			require.NoError(t, os.MkdirAll(filepath.Join(dst, ".git"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dst, "go.mod"),
				[]byte("module "+module+"\n\ngo 1.25\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dst, ManifestFile), []byte(manifest), 0o644))
			return "", nil
		case name == "git" && args[0] == "rev-parse":
			return "cafebabecafebabecafebabecafebabecafebabe\n", nil
		case name == "git":
			return "", nil // fetch / checkout
		case name == "go" && len(args) >= 2 && args[0] == "mod" && args[1] == "edit":
			calls.goModEdits = append(calls.goModEdits, strings.Join(args, " "))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
				[]byte("module github.com/beelzebub-labs/beelzebub/v3\n\ngo 1.25\n\nreplace "+module+" => ./plugins/scanner\n"), 0o644))
			return "", nil
		case name == "go" && len(args) >= 2 && args[0] == "mod" && args[1] == "tidy":
			if calls.failTidy {
				return "", fmt.Errorf("tidy failed")
			}

			calls.tidied++
			return "", nil
		case name == "go" && len(args) >= 1 && args[0] == "build":
			calls.built++
			return "", nil
		case name == "docker" || name == "docker-compose":
			calls.dockerCmds = append(calls.dockerCmds, strings.Join(args, " "))
			return "", nil
		default:
			return "", fmt.Errorf("unexpected command: %s %v", name, args)
		}
	}

	m := &Manager{
		moduleRoot:  root,
		coreModule:  "github.com/beelzebub-labs/beelzebub/v3",
		coreVersion: "dev",
		run:         run,
	}
	return m, calls
}

type fakeCalls struct {
	goModEdits []string
	tidied     int
	built      int
	dockerCmds []string
	failTidy   bool
}

func TestNewManager_SuccessAndNoModuleRoot(t *testing.T) {
	root := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module github.com/beelzebub-labs/beelzebub/v3\n"), 0o644))
	t.Chdir(root)

	m, err := NewManager("dev", "token")
	require.NoError(t, err)
	assert.Equal(t, root, m.moduleRoot)
	assert.Equal(t, "github.com/beelzebub-labs/beelzebub/v3", m.coreModule)
	assert.Equal(t, "token", m.token)

	empty := t.TempDir()
	t.Chdir(empty)
	_, err = NewManager("dev", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no go.mod")
}

func TestApply_LocalBuilds(t *testing.T) {
	m, calls := fakeManager(t, "github.com/acme/scanner", validManifest)
	mode, err := m.Apply(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "local", mode)
	assert.Equal(t, 1, calls.built)
	assert.Empty(t, calls.dockerCmds)
}

func TestApply_DockerRebuilds(t *testing.T) {
	m, calls := fakeManager(t, "github.com/acme/scanner", validManifest)
	require.NoError(t, os.WriteFile(filepath.Join(m.moduleRoot, ModeFileName), []byte("docker\n"), 0o644))

	mode, err := m.Apply(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "docker", mode)
	assert.Equal(t, 0, calls.built)
	assert.Contains(t, strings.Join(calls.dockerCmds, "\n"), "up -d --build")
}

func TestApply_DockerFallbackAndErrors(t *testing.T) {
	m, calls := fakeManager(t, "github.com/acme/scanner", validManifest)
	require.NoError(t, os.WriteFile(filepath.Join(m.moduleRoot, ModeFileName), []byte("docker\n"), 0o644))

	m.run = func(_ context.Context, _ string, name string, args ...string) (string, error) {
		switch {
		case name == "docker" && strings.Join(args, " ") == "compose version":
			return "", fmt.Errorf("compose v2 missing")

		case name == "docker-compose":
			calls.dockerCmds = append(calls.dockerCmds, strings.Join(args, " "))
			return "", nil

		default:
			return "", fmt.Errorf("unexpected command: %s %v", name, args)
		}
	}

	mode, err := m.Apply(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "docker", mode)
	assert.Contains(t, strings.Join(calls.dockerCmds, "\n"), "up -d --build")

	m.run = func(context.Context, string, string, ...string) (string, error) {
		return "", fmt.Errorf("compose failed")
	}

	mode, err = m.Apply(context.Background())
	require.Error(t, err)
	assert.Equal(t, "docker", mode)
	assert.Contains(t, err.Error(), "docker compose up")
}

func TestBuild_Error(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)

	m.run = func(context.Context, string, string, ...string) (string, error) {
		return "", fmt.Errorf("build failed")
	}

	err := m.Build(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "go build")
}

func TestInstallMode_DefaultsLocal(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	assert.Equal(t, "local", m.installMode())
}

const validManifest = `
name: scanner
version: 1.2.0
module: github.com/acme/scanner
`

func TestManager_InstallRemove(t *testing.T) {
	m, calls := fakeManager(t, "github.com/acme/scanner", validManifest)
	ctx := context.Background()

	locked, err := m.Install(ctx, "github.com/acme/scanner", "", false)
	require.NoError(t, err)
	assert.Equal(t, "scanner", locked.Name)
	assert.Equal(t, "github.com/acme/scanner", locked.Import)
	assert.Equal(t, "plugins/scanner", locked.Dir)
	assert.NotEmpty(t, locked.Commit)

	// Plugin vendored, .git stripped.
	dest := filepath.Join(m.moduleRoot, "plugins", "scanner")
	assert.DirExists(t, dest)
	assert.NoDirExists(t, filepath.Join(dest, ".git"))
	assert.FileExists(t, filepath.Join(dest, "go.mod"))

	// Generated imports blank-import the plugin.
	gen, err := os.ReadFile(filepath.Join(m.moduleRoot, "plugins", generatedFileName))
	require.NoError(t, err)
	assert.Contains(t, string(gen), `_ "github.com/acme/scanner"`)

	// go.mod replace added and tidied.
	assert.Contains(t, strings.Join(calls.goModEdits, "\n"), "-replace=github.com/acme/scanner=./plugins/scanner")
	assert.GreaterOrEqual(t, calls.tidied, 1)

	// Lockfile recorded it.
	lf, err := LoadLockFile(m.lockPath())
	require.NoError(t, err)
	require.Len(t, lf.Plugins, 1)
	assert.Equal(t, "scanner", lf.Plugins[0].Name)

	// --- Remove reverses everything ---
	_, err = m.Remove(ctx, "scanner")
	require.NoError(t, err)
	assert.NoDirExists(t, dest)
	assert.Contains(t, strings.Join(calls.goModEdits, "\n"), "-dropreplace=github.com/acme/scanner")

	gen, err = os.ReadFile(filepath.Join(m.moduleRoot, "plugins", generatedFileName))
	require.NoError(t, err)
	assert.NotContains(t, string(gen), "github.com/acme/scanner")

	lf, err = LoadLockFile(m.lockPath())
	require.NoError(t, err)
	assert.Empty(t, lf.Plugins)
}

func TestManager_Install_AlreadyInstalled(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	ctx := context.Background()

	_, err := m.Install(ctx, "github.com/acme/scanner", "", false)
	require.NoError(t, err)

	// Second install without --force fails.
	_, err = m.Install(ctx, "github.com/acme/scanner", "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already installed")

	// With --force it succeeds.
	_, err = m.Install(ctx, "github.com/acme/scanner", "", true)
	require.NoError(t, err)
}

func TestManager_InstallForce_RestoresExistingDirOnRegenerateFailure(t *testing.T) {
	m, calls := fakeManager(t, "github.com/acme/scanner", validManifest)
	ctx := context.Background()

	_, err := m.Install(ctx, "github.com/acme/scanner", "", false)
	require.NoError(t, err)

	dest := filepath.Join(m.moduleRoot, "plugins", "scanner")
	marker := filepath.Join(dest, "kept.txt")
	require.NoError(t, os.WriteFile(marker, []byte("old plugin"), 0o644))
	goModPath := filepath.Join(m.moduleRoot, "go.mod")
	goModBefore, readErr := os.ReadFile(goModPath)
	require.NoError(t, readErr)

	calls.failTidy = true
	_, err = m.Install(ctx, "github.com/acme/scanner", "", true)
	require.Error(t, err)
	assert.FileExists(t, marker)

	raw, readErr := os.ReadFile(marker)
	require.NoError(t, readErr)
	assert.Equal(t, "old plugin", string(raw))

	goModAfter, readErr := os.ReadFile(goModPath)
	require.NoError(t, readErr)
	assert.Equal(t, string(goModBefore), string(goModAfter))
}

func TestManager_Install_ManifestMismatch(t *testing.T) {
	// Manifest module disagrees with the (faked) go.mod module.
	m, _ := fakeManager(t, "github.com/acme/other", validManifest)
	_, err := m.Install(context.Background(), "github.com/acme/scanner", "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match go.mod")

	// Nothing should have been vendored.
	assert.NoDirExists(t, filepath.Join(m.moduleRoot, "plugins", "scanner"))
}

func TestManager_Install_ParseAndCloneErrors(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	_, err := m.Install(context.Background(), "github.com/acme", "", false)
	require.Error(t, err)

	m.run = func(context.Context, string, string, ...string) (string, error) {
		return "", fmt.Errorf("clone failed")
	}

	_, err = m.Install(context.Background(), "github.com/acme/scanner", "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clone failed")
}

func TestManager_Remove_ErrorPaths(t *testing.T) {
	m, calls := fakeManager(t, "github.com/acme/scanner", validManifest)
	ctx := context.Background()

	_, err := m.Remove(ctx, "ghost")
	require.ErrorIs(t, err, ErrNotInstalled)

	_, err = m.Install(ctx, "github.com/acme/scanner", "", false)
	require.NoError(t, err)

	m.run = func(_ context.Context, dir, name string, args ...string) (string, error) {
		if name == "go" && len(args) >= 2 && args[0] == "mod" && args[1] == "edit" {
			return "", fmt.Errorf("edit failed")
		}

		return calls.runOriginal(t, dir, name, args...)
	}

	_, err = m.Remove(ctx, "scanner")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "editing go.mod")
}

func (f *fakeCalls) runOriginal(t *testing.T, dir, name string, args ...string) (string, error) {
	t.Helper()
	return "", fmt.Errorf("unexpected command: %s %v in %s", name, args, dir)
}

func TestManager_List_InvalidLockfile(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	require.NoError(t, os.MkdirAll(filepath.Dir(m.lockPath()), 0o755))
	require.NoError(t, os.WriteFile(m.lockPath(), []byte("plugins:\n  - name: [bad\n"), 0o644))

	_, err := m.List()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing")
}

func TestManager_Regenerate_GoModEditError(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	m.run = func(context.Context, string, string, ...string) (string, error) {
		return "", fmt.Errorf("edit failed")
	}

	err := m.regenerate(context.Background(), &LockFile{Plugins: []LockedPlugin{{
		Name:   "scanner",
		Module: "github.com/acme/scanner",
		Dir:    "plugins/scanner",
		Import: "github.com/acme/scanner",
	}}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "adding go.mod replace")
}

func TestReplacePluginDir_RollbackAndCommit(t *testing.T) {
	staging := t.TempDir()
	src := filepath.Join(staging, "src")
	dest := filepath.Join(t.TempDir(), "scanner")
	require.NoError(t, os.MkdirAll(src, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "new.txt"), []byte("new plugin"), 0o644))
	require.NoError(t, os.MkdirAll(dest, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dest, "old.txt"), []byte("old plugin"), 0o644))

	rollback, commit, err := replacePluginDir(src, dest, staging, true)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(dest, "new.txt"))
	assert.NoFileExists(t, filepath.Join(dest, "old.txt"))

	require.NoError(t, rollback())
	assert.FileExists(t, filepath.Join(dest, "old.txt"))
	assert.NoFileExists(t, filepath.Join(dest, "new.txt"))

	src = filepath.Join(staging, "src2")
	require.NoError(t, os.MkdirAll(src, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "new.txt"), []byte("new plugin"), 0o644))
	rollback, commit, err = replacePluginDir(src, dest, staging, true)
	require.NoError(t, err)
	require.NoError(t, commit())
	assert.FileExists(t, filepath.Join(dest, "new.txt"))
	assert.NoFileExists(t, filepath.Join(staging, "previous", "old.txt"))
	_ = rollback
}

func TestReplacePluginDir_RejectsExistingWithoutForce(t *testing.T) {
	staging := t.TempDir()
	src := filepath.Join(staging, "src")
	dest := filepath.Join(t.TempDir(), "scanner")
	require.NoError(t, os.MkdirAll(src, 0o755))
	require.NoError(t, os.MkdirAll(dest, 0o755))

	_, _, err := replacePluginDir(src, dest, staging, false)
	require.ErrorIs(t, err, ErrAlreadyInstalled)
}

func TestModuleFileSnapshot_Restore(t *testing.T) {
	root := t.TempDir()
	goMod := filepath.Join(root, "go.mod")
	goSum := filepath.Join(root, "go.sum")
	require.NoError(t, os.WriteFile(goMod, []byte("module before\n"), 0o644))

	snapshot, err := snapshotModuleFiles(root)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(goMod, []byte("module after\n"), 0o644))
	require.NoError(t, os.WriteFile(goSum, []byte("new checksum\n"), 0o644))
	require.NoError(t, snapshot.restore())

	raw, err := os.ReadFile(goMod)
	require.NoError(t, err)
	assert.Equal(t, "module before\n", string(raw))
	assert.NoFileExists(t, goSum)
	assert.NoError(t, (*moduleFileSnapshot)(nil).restore())
}

func TestFindModuleRootAndReadModulePath(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	goMod := filepath.Join(root, "go.mod")
	require.NoError(t, os.WriteFile(goMod, []byte("module github.com/acme/root\n\ngo 1.25\n"), 0o644))

	got, err := findModuleRoot(nested)
	require.NoError(t, err)
	assert.Equal(t, root, got)

	module, err := readModulePath(goMod)
	require.NoError(t, err)
	assert.Equal(t, "github.com/acme/root", module)

	_, err = readModulePath(filepath.Join(root, "missing.mod"))
	require.Error(t, err)

	badMod := filepath.Join(root, "bad.mod")
	require.NoError(t, os.WriteFile(badMod, []byte("go 1.25\n"), 0o644))
	_, err = readModulePath(badMod)
	require.Error(t, err)
}

func TestManager_List(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	_, err := m.Install(context.Background(), "github.com/acme/scanner", "", false)
	require.NoError(t, err)

	list, err := m.List()
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "scanner", list[0].Name)
}
