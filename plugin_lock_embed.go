package main

import (
	"embed"
	"os"
	"path/filepath"
)

//go:embed configurations/*.yaml
var embeddedConfigurations embed.FS

func persistEmbeddedPluginLockfile() {
	raw, err := embeddedConfigurations.ReadFile("configurations/lockfile.yaml")
	if err != nil {
		return
	}

	path := os.Getenv("BEELZEBUB_PLUGIN_LOCKFILE")
	if path == "" {
		path = "/configurations/lockfile.yaml"
	}

	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		return
	}

	_ = os.WriteFile(path, raw, 0o644)
}
