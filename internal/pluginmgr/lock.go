package pluginmgr

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const LockFileName = "lockfile.yaml"

type LockedPlugin struct {
	Name       string `yaml:"name"`
	Module     string `yaml:"module"`
	Version    string `yaml:"version"`
	Import     string `yaml:"import"`
	Dir        string `yaml:"dir"`
	Source     string `yaml:"source"`
	Declared   string `yaml:"declared,omitempty"` // the raw source as written in configurations/plugins.yaml
	Ref        string `yaml:"ref,omitempty"`      // requested tag/branch/commit ("" = default branch)
	Commit     string `yaml:"commit"`
	Entrypoint string `yaml:"entrypoint,omitempty"`
}

type LockFile struct {
	Plugins []LockedPlugin `yaml:"plugins"`
}

func LoadLockFile(path string) (*LockFile, error) {
	lf := &LockFile{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return lf, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := yaml.Unmarshal(raw, lf); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return lf, nil
}

func (lf *LockFile) Clone() *LockFile {
	if lf == nil {
		return &LockFile{}
	}

	out := &LockFile{Plugins: make([]LockedPlugin, len(lf.Plugins))}
	copy(out.Plugins, lf.Plugins)
	return out
}

func (lf *LockFile) Save(path string) error {
	lf.sort()
	raw, err := yaml.Marshal(lf)
	if err != nil {
		return fmt.Errorf("marshaling lockfile: %w", err)
	}
	header := "# Managed by `beelzebub plugin`. Do not edit by hand.\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating lockfile dir: %w", err)
	}
	if err := os.WriteFile(path, append([]byte(header), raw...), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

func (lf *LockFile) Find(name string) (LockedPlugin, bool) {
	for _, p := range lf.Plugins {
		if p.Name == name {
			return p, true
		}
	}
	return LockedPlugin{}, false
}

func (lf *LockFile) Upsert(p LockedPlugin) {
	for i := range lf.Plugins {
		if lf.Plugins[i].Name == p.Name {
			lf.Plugins[i] = p
			return
		}
	}
	lf.Plugins = append(lf.Plugins, p)
	lf.sort()
}

func (lf *LockFile) Remove(name string) bool {
	for i := range lf.Plugins {
		if lf.Plugins[i].Name == name {
			lf.Plugins = append(lf.Plugins[:i], lf.Plugins[i+1:]...)
			return true
		}
	}
	return false
}

func (lf *LockFile) sort() {
	sort.Slice(lf.Plugins, func(i, j int) bool {
		return strings.ToLower(lf.Plugins[i].Name) < strings.ToLower(lf.Plugins[j].Name)
	})
}
