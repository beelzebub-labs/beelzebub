package main

import (
	"embed"
	"os"
	"path/filepath"
	"testing"
)

func TestPersistEmbeddedPluginLockfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lockfile.yaml")
	t.Setenv("BEELZEBUB_PLUGIN_LOCKFILE", path)

	persistEmbeddedPluginLockfile()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	want, err := embeddedConfigurations.ReadFile("configurations/lockfile.yaml")
	if err != nil {
		t.Fatalf("ReadFile embedded lockfile: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("persisted lockfile differs from embedded lockfile")
	}
}

func TestPersistEmbeddedPluginLockfile_MissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "lockfile.yaml")
	t.Setenv("BEELZEBUB_PLUGIN_LOCKFILE", path)

	persistEmbeddedPluginLockfile()

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected lockfile not to be created, got err=%v", err)
	}
}

func TestMain_ReportsCLIError(t *testing.T) {
	oldArgs := os.Args
	oldExit := exitProcess
	t.Cleanup(func() {
		os.Args = oldArgs
		exitProcess = oldExit
	})

	exitCode := 0
	exitProcess = func(code int) { exitCode = code }
	os.Args = []string{"beelzebub", "--definitely-invalid"}

	main()

	if exitCode != 1 {
		t.Fatalf("main exit code = %d, want 1", exitCode)
	}
}

func TestMain_VersionSuccess(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"beelzebub", "version"}
	main()
}

func TestPersistEmbeddedPluginLockfile_WriteError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lockfile-dir")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEELZEBUB_PLUGIN_LOCKFILE", path)
	persistEmbeddedPluginLockfile()
}

func TestPersistEmbeddedPluginLockfile_EmbeddedReadError(t *testing.T) {
	old := embeddedConfigurations
	embeddedConfigurations = embed.FS{}
	t.Cleanup(func() { embeddedConfigurations = old })

	persistEmbeddedPluginLockfile()
}

func TestMain_RunPersistsLockfileBeforeCLI(t *testing.T) {
	oldArgs := os.Args
	oldExit := exitProcess
	t.Cleanup(func() {
		os.Args = oldArgs
		exitProcess = oldExit
	})

	lockfile := filepath.Join(t.TempDir(), "lockfile.yaml")
	t.Setenv("BEELZEBUB_PLUGIN_LOCKFILE", lockfile)
	exitProcess = func(int) {}
	os.Args = []string{"beelzebub", "run", "--help"}

	main()

	if _, err := os.Stat(lockfile); err != nil {
		t.Fatalf("main did not persist embedded lockfile: %v", err)
	}
}

func TestPersistEmbeddedPluginLockfile_DefaultPath(t *testing.T) {
	t.Setenv("BEELZEBUB_PLUGIN_LOCKFILE", "")
	persistEmbeddedPluginLockfile()
}
