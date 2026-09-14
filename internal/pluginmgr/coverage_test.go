package pluginmgr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecRunner_SuccessAndFailure(t *testing.T) {
	dir := t.TempDir()

	out, err := execRunner(context.Background(), dir, "sh", "-c", "printf success")
	require.NoError(t, err)
	if out != "success" {
		t.Fatalf("execRunner output = %q, want success", out)
	}

	out, err = execRunner(context.Background(), dir, "sh", "-c", "printf failure >&2; exit 1")
	require.Error(t, err)
	if out != "failure" {
		t.Fatalf("execRunner failure output = %q, want failure", out)
	}
	if !strings.Contains(err.Error(), "sh -c") || !strings.Contains(err.Error(), "failure") {
		t.Fatalf("execRunner failure = %v", err)
	}
}

func TestLockFileSave_CreateDirectoryError(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "not-a-directory")
	require.NoError(t, os.WriteFile(blocker, []byte("file"), 0o644))

	err := (&LockFile{}).Save(filepath.Join(blocker, LockFileName))
	require.Error(t, err)
	if !strings.Contains(err.Error(), "creating lockfile dir") {
		t.Fatalf("Save error = %v", err)
	}
}

func TestWriteImportsFile_CreateDirectoryError(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "not-a-directory")
	require.NoError(t, os.WriteFile(blocker, []byte("file"), 0o644))

	err := writeImportsFile(filepath.Join(blocker, "plugins"), &LockFile{})
	require.Error(t, err)
	if !strings.Contains(err.Error(), "creating plugins dir") {
		t.Fatalf("writeImportsFile error = %v", err)
	}
}

func TestSaveDeclaredFile_CreateDirectoryError(t *testing.T) {
	root := t.TempDir()
	configurations := filepath.Join(root, ConfigurationsDirName)
	require.NoError(t, os.WriteFile(configurations, []byte("file"), 0o644))

	m := &Manager{moduleRoot: root}
	err := m.saveDeclaredFile(&declaredFile{})
	require.Error(t, err)
	if !strings.Contains(err.Error(), "creating") {
		t.Fatalf("saveDeclaredFile error = %v", err)
	}
}

func TestSeedConfig_NewExistingAndMissing(t *testing.T) {
	root := t.TempDir()
	m := &Manager{moduleRoot: root}
	sourceDir := filepath.Join(root, "plugins", "scanner", ConfigurationsDirName, "plugins")
	require.NoError(t, os.MkdirAll(sourceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "scanner.yaml"), []byte("enabled: true\n"), 0o644))

	path, created := m.seedConfig("scanner", filepath.ToSlash(filepath.Join(PluginsDirName, "scanner")))
	if path != filepath.ToSlash(filepath.Join(DeclaredConfigDir, "scanner.yaml")) || !created {
		t.Fatalf("seedConfig new = (%q, %t)", path, created)
	}

	path, created = m.seedConfig("scanner", filepath.ToSlash(filepath.Join(PluginsDirName, "scanner")))
	if path == "" || created {
		t.Fatalf("seedConfig existing = (%q, %t)", path, created)
	}

	path, created = m.seedConfig("missing", filepath.ToSlash(filepath.Join(PluginsDirName, "missing")))
	if path != "" || created {
		t.Fatalf("seedConfig missing = (%q, %t)", path, created)
	}
}

func TestManager_InstallFailureMatrix(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, m *Manager, base Runner)
		want  string
	}{
		{
			name:  "invalid source",
			setup: func(t *testing.T, m *Manager, base Runner) {},
			want:  "empty plugin source",
		},
		{
			name: "plugins directory cannot be created",
			setup: func(t *testing.T, m *Manager, base Runner) {
				require.NoError(t, os.WriteFile(m.pluginsDir(), []byte("blocker"), 0o644))
			},
			want: "creating plugins dir",
		},
		{
			name: "clone failure",
			setup: func(t *testing.T, m *Manager, base Runner) {
				m.run = func(context.Context, string, string, ...string) (string, error) {
					return "", errors.New("clone failed")
				}
			},
			want: "clone failed",
		},
		{
			name: "missing go.mod",
			setup: func(t *testing.T, m *Manager, base Runner) {
				m.run = func(ctx context.Context, dir, name string, args ...string) (string, error) {
					out, err := base(ctx, dir, name, args...)
					if name == "git" && len(args) > 0 && args[0] == "clone" {
						_ = os.Remove(filepath.Join(args[len(args)-1], "go.mod"))
					}
					return out, err
				}
			},
			want: "no valid go.mod",
		},
		{
			name: "missing manifest",
			setup: func(t *testing.T, m *Manager, base Runner) {
				m.run = func(ctx context.Context, dir, name string, args ...string) (string, error) {
					out, err := base(ctx, dir, name, args...)
					if name == "git" && len(args) > 0 && args[0] == "clone" {
						_ = os.Remove(filepath.Join(args[len(args)-1], ManifestFile))
					}
					return out, err
				}
			},
			want: "missing plugins.yaml",
		},
		{
			name: "manifest validation",
			setup: func(t *testing.T, m *Manager, base Runner) {
				m.run = func(ctx context.Context, dir, name string, args ...string) (string, error) {
					out, err := base(ctx, dir, name, args...)
					if name == "git" && len(args) > 0 && args[0] == "clone" {
						require.NoError(t, os.WriteFile(filepath.Join(args[len(args)-1], ManifestFile), []byte("name: BAD\n"), 0o644))
					}
					return out, err
				}
			},
			want: "name \"BAD\" is invalid",
		},
		{
			name: "head commit failure",
			setup: func(t *testing.T, m *Manager, base Runner) {
				m.run = func(ctx context.Context, dir, name string, args ...string) (string, error) {
					if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
						return "", errors.New("rev-parse failed")
					}
					return base(ctx, dir, name, args...)
				}
			},
			want: "rev-parse failed",
		},
		{
			name: "invalid lockfile",
			setup: func(t *testing.T, m *Manager, base Runner) {
				require.NoError(t, os.MkdirAll(filepath.Dir(m.lockPath()), 0o755))
				require.NoError(t, os.WriteFile(m.lockPath(), []byte("plugins: ["), 0o644))
			},
			want: "parsing",
		},
		{
			name: "regenerate failure",
			setup: func(t *testing.T, m *Manager, base Runner) {
				m.run = func(ctx context.Context, dir, name string, args ...string) (string, error) {
					if name == "go" && len(args) > 1 && args[0] == "mod" && args[1] == "edit" {
						return "", errors.New("mod edit failed")
					}
					return base(ctx, dir, name, args...)
				}
			},
			want: "adding go.mod replace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
			base := m.run
			if tt.name == "invalid source" {
				_, err := m.Install(context.Background(), "", "", false)
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.want)
				return
			}
			tt.setup(t, m, base)
			_, err := m.Install(context.Background(), "github.com/acme/scanner", "", false)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestLockFileAndReplaceFailurePaths(t *testing.T) {
	root := t.TempDir()
	badLock := filepath.Join(root, "bad-lock.yaml")
	require.NoError(t, os.WriteFile(badLock, []byte("plugins: ["), 0o644))
	_, err := LoadLockFile(badLock)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing")

	blocker := filepath.Join(root, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("file"), 0o644))
	_, _, err = replacePluginDir(filepath.Join(root, "missing"), filepath.Join(blocker, "dest"), filepath.Join(root, "stage"), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking existing plugin dir")

	src := filepath.Join(root, "src")
	dest := filepath.Join(root, "dest")
	stage := filepath.Join(root, "stage2")
	require.NoError(t, os.MkdirAll(src, 0o755))
	require.NoError(t, os.MkdirAll(dest, 0o755))
	_, _, err = replacePluginDir(src, dest, stage, false)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAlreadyInstalled)
}

func TestSeedConfig_MkdirError(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	source := filepath.Join(m.moduleRoot, "plugins", "scanner", DeclaredConfigDir)
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "scanner.yaml"), []byte("protocol: ssh\n"), 0o644))
	blocker := filepath.Join(m.moduleRoot, ConfigurationsDirName, "plugins")
	require.NoError(t, os.RemoveAll(blocker))
	require.NoError(t, os.MkdirAll(filepath.Dir(blocker), 0o755))
	require.NoError(t, os.WriteFile(blocker, []byte("blocked"), 0o644))

	path, created := m.seedConfig("scanner", "plugins/scanner")
	assert.Empty(t, path)
	assert.False(t, created)
}

func TestInstall_SnapshotError(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	require.NoError(t, os.Mkdir(filepath.Join(m.moduleRoot, "go.sum"), 0o755))

	_, err := m.Install(context.Background(), "github.com/acme/scanner", "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "snapshotting go.sum")
}

func TestManager_Remove_FinalizationErrors(t *testing.T) {
	t.Run("regenerate", func(t *testing.T) {
		m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
		_, err := m.Install(context.Background(), "github.com/acme/scanner", "", false)
		require.NoError(t, err)
		m.run = func(_ context.Context, _ string, name string, args ...string) (string, error) {
			if name == "go" && len(args) >= 2 && args[0] == "mod" && args[1] == "tidy" {
				return "", errors.New("tidy failed")
			}
			return "", nil
		}
		_, err = m.Remove(context.Background(), "scanner")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "go mod tidy")
	})

	t.Run("save lockfile", func(t *testing.T) {
		m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
		_, err := m.Install(context.Background(), "github.com/acme/scanner", "", false)
		require.NoError(t, err)
		lockPath := m.lockPath()
		m.run = func(_ context.Context, _ string, name string, args ...string) (string, error) {
			if name == "go" && len(args) >= 2 && args[0] == "mod" && args[1] == "edit" {
				_ = os.Remove(lockPath)
				if mkdirErr := os.Mkdir(lockPath, 0o755); mkdirErr != nil {
					return "", mkdirErr
				}
				return "", nil
			}
			return "", nil
		}
		_, err = m.Remove(context.Background(), "scanner")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "writing")
	})

	t.Run("undeclare", func(t *testing.T) {
		m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
		_, err := m.Install(context.Background(), "github.com/acme/scanner", "", false)
		require.NoError(t, err)
		declaredPath := m.declaredPath()
		m.run = func(_ context.Context, _ string, name string, args ...string) (string, error) {
			if name == "go" && len(args) >= 2 && args[0] == "mod" && args[1] == "tidy" {
				require.NoError(t, os.MkdirAll(filepath.Dir(declaredPath), 0o755))
				return "", os.Mkdir(declaredPath, 0o755)
			}
			return "", nil
		}
		_, err = m.Remove(context.Background(), "scanner")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading")
	})
}

func TestInstallMode_ExplicitLocal(t *testing.T) {
	m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
	require.NoError(t, os.WriteFile(filepath.Join(m.moduleRoot, ModeFileName), []byte("local\n"), 0o644))
	assert.Equal(t, "local", m.installMode())
}

func TestPluginManager_AdditionalUtilityErrors(t *testing.T) {
	t.Run("invalid declared source", func(t *testing.T) {
		m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
		require.NoError(t, os.MkdirAll(filepath.Dir(m.declaredPath()), 0o755))
		require.NoError(t, os.WriteFile(m.declaredPath(), []byte("plugins:\n  - source: github.com/acme\n"), 0o644))
		_, err := m.Update(context.Background(), "")
		require.Error(t, err)
	})

	t.Run("invalid lock index", func(t *testing.T) {
		m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
		require.NoError(t, os.MkdirAll(filepath.Dir(m.lockPath()), 0o755))
		require.NoError(t, os.WriteFile(m.lockPath(), []byte("plugins:\n  - name: [bad\n"), 0o644))
		_, err := m.Update(context.Background(), "")
		require.Error(t, err)
	})

	t.Run("manifest read error", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dir, ManifestFile), 0o755))
		_, err := LoadManifest(dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading")
	})

	t.Run("restore write error", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(root, "go.mod"), 0o755))
		raw := []byte("module before\n")
		snapshot := &moduleFileSnapshot{root: root, files: map[string]*[]byte{"go.mod": &raw}}
		err := snapshot.restore()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "restoring go.mod")
	})

	t.Run("replace move error", func(t *testing.T) {
		staging := t.TempDir()
		src := filepath.Join(staging, "missing")
		dest := filepath.Join(t.TempDir(), "dest")
		_, _, err := replacePluginDir(src, dest, staging, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "moving plugin into place")
	})

	t.Run("filesystem write errors", func(t *testing.T) {
		m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
		parentFile := filepath.Join(m.moduleRoot, "not-a-directory")
		require.NoError(t, os.WriteFile(parentFile, []byte("blocked"), 0o644))
		lf := &LockFile{}
		err := lf.Save(filepath.Join(parentFile, LockFileName))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "creating lockfile dir")

		pluginsDir := m.pluginsDir()
		require.NoError(t, os.MkdirAll(pluginsDir, 0o755))
		require.NoError(t, os.Mkdir(filepath.Join(pluginsDir, generatedFileName), 0o755))
		err = writeImportsFile(pluginsDir, &LockFile{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "writing")

		require.NoError(t, os.MkdirAll(filepath.Dir(m.declaredPath()), 0o755))
		require.NoError(t, os.Mkdir(m.declaredPath(), 0o755))
		err = m.saveDeclaredFile(&declaredFile{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "writing")
	})

	t.Run("local source points to file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "plugin")
		require.NoError(t, os.WriteFile(file, []byte("not a directory"), 0o644))
		_, err := ParseSource(file)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a directory")
	})

	t.Run("replace backup conflict", func(t *testing.T) {
		staging := t.TempDir()
		src := filepath.Join(staging, "src")
		dest := filepath.Join(t.TempDir(), "dest")
		require.NoError(t, os.MkdirAll(src, 0o755))
		require.NoError(t, os.MkdirAll(dest, 0o755))
		require.NoError(t, os.Mkdir(filepath.Join(staging, "previous"), 0o755))
		_, _, err := replacePluginDir(src, dest, staging, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "moving existing plugin aside")
	})

	t.Run("lock and regenerate write errors", func(t *testing.T) {
		m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
		lf := &LockFile{}
		lockDir := filepath.Join(t.TempDir(), "lockfile.yaml")
		require.NoError(t, os.Mkdir(lockDir, 0o755))
		err := lf.Save(lockDir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "writing")

		pluginsPath := m.pluginsDir()
		require.NoError(t, os.WriteFile(pluginsPath, []byte("blocked"), 0o644))
		err = m.regenerate(context.Background(), &LockFile{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "creating plugins dir")
	})

	t.Run("manager reads lock directory as error", func(t *testing.T) {
		m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
		require.NoError(t, os.MkdirAll(filepath.Dir(m.lockPath()), 0o755))
		require.NoError(t, os.Mkdir(m.lockPath(), 0o755))
		_, err := m.List()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading")
	})

	t.Run("git ref checkout error after unshallow fallback", func(t *testing.T) {
		m := &Manager{run: func(_ context.Context, _ string, name string, args ...string) (string, error) {
			switch {
			case name == "git" && args[0] == "clone":
				return "", nil
			case name == "git" && args[0] == "fetch" && args[1] == "--depth":
				return "", errors.New("shallow fetch failed")
			case name == "git" && args[0] == "fetch" && args[1] == "--unshallow":
				return "", nil
			case name == "git" && args[0] == "checkout":
				return "", errors.New("checkout failed")
			default:
				return "", nil
			}
		}}
		err := m.clone(context.Background(), RepoSource{CloneURL: "https://example.test/acme/plugin.git", Host: "example.test"}, "v1", filepath.Join(t.TempDir(), "checkout"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "checkout failed")
	})

	t.Run("github clone failure explains authentication", func(t *testing.T) {
		m := &Manager{run: func(context.Context, string, string, ...string) (string, error) {
			return "", errors.New("clone failed")
		}}
		err := m.clone(context.Background(), RepoSource{
			CloneURL: "https://github.com/acme/plugin.git",
			Host:     "github.com",
			Owner:    "acme",
			Repo:     "plugin",
		}, "", filepath.Join(t.TempDir(), "checkout"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pass --token")
	})

	t.Run("manifest path normalization edges", func(t *testing.T) {
		assert.Equal(t, ".", cleanEntrypoint(" ./ "))
		require.Error(t, validateEntrypoint("../cmd"))
		require.Error(t, validateEntrypoint("cmd/../.git"))
	})

	t.Run("manager rejects go.mod without module", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("go 1.25\n"), 0o644))
		t.Chdir(root)
		_, err := NewManager("dev", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no module directive")
	})

	t.Run("install declared reads lockfile error", func(t *testing.T) {
		m, _ := fakeManager(t, "github.com/acme/scanner", validManifest)
		require.NoError(t, os.MkdirAll(filepath.Dir(m.declaredPath()), 0o755))
		require.NoError(t, os.WriteFile(m.declaredPath(), []byte("plugins:\n  - source: github.com/acme/scanner\n"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Dir(m.lockPath()), 0o755))
		require.NoError(t, os.Mkdir(m.lockPath(), 0o755))
		_, err := m.InstallDeclared(context.Background(), false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading")
	})
}
