package main

import (
	"embed"
	"os"
	"path/filepath"
	"testing"
)

func TestPersistEmbeddedPluginLockfileWritesConfiguredPath(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "lockfile.yaml")
	t.Setenv("BEELZEBUB_PLUGIN_LOCKFILE", dst)

	persistEmbeddedPluginLockfile()

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading persisted lockfile: %v", err)
	}

	want, err := embeddedConfigurations.ReadFile("configurations/lockfile.yaml")
	if err != nil {
		t.Fatalf("reading embedded lockfile: %v", err)
	}

	if string(got) != string(want) {
		t.Fatalf("persisted lockfile = %q, want %q", got, want)
	}
}

func TestPersistEmbeddedPluginLockfileSkipsMissingDirectory(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "missing", "lockfile.yaml")
	t.Setenv("BEELZEBUB_PLUGIN_LOCKFILE", dst)

	persistEmbeddedPluginLockfile()

	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("lockfile stat error = %v, want not exist", err)
	}
}

func TestPersistEmbeddedPluginLockfileSkipsMissingEmbeddedFile(t *testing.T) {
	oldEmbedded := embeddedConfigurations
	embeddedConfigurations = embed.FS{}
	t.Cleanup(func() { embeddedConfigurations = oldEmbedded })

	dst := filepath.Join(t.TempDir(), "lockfile.yaml")
	t.Setenv("BEELZEBUB_PLUGIN_LOCKFILE", dst)

	persistEmbeddedPluginLockfile()

	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("lockfile stat error = %v, want not exist", err)
	}
}
