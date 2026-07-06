// Package pluginmgr installs, wires, and removes plugins fetched from git.
package pluginmgr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Expected, non-fatal conditions — the CLI surfaces these as calm messages
// rather than errors.
var (
	ErrAlreadyInstalled = errors.New("already installed")
	ErrNotInstalled     = errors.New("not installed")
)

const PluginsDirName = "plugins"

type Manager struct {
	moduleRoot  string
	coreModule  string
	coreVersion string
	token       string
	run         Runner
}

func NewManager(coreVersion, token string) (*Manager, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root, err := findModuleRoot(wd)
	if err != nil {
		return nil, err
	}
	coreModule, err := readModulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}
	return &Manager{
		moduleRoot:  root,
		coreModule:  coreModule,
		coreVersion: coreVersion,
		token:       token,
		run:         execRunner,
	}, nil
}

func (m *Manager) pluginsDir() string { return filepath.Join(m.moduleRoot, PluginsDirName) }
func (m *Manager) lockPath() string   { return filepath.Join(m.pluginsDir(), LockFileName) }

// Install clones rawSource, validates its manifest, vendors it under plugins/<name> and wires it in.
func (m *Manager) Install(ctx context.Context, rawSource, ref string, force bool) (LockedPlugin, error) {
	src, err := ParseSource(rawSource)
	if err != nil {
		return LockedPlugin{}, err
	}
	if src.Ref != "" && ref == "" {
		ref = src.Ref
	}

	// Stage the clone; only promote it into place once validation passes.
	staging, err := os.MkdirTemp(m.pluginsDir(), ".staging-")
	if err != nil {
		if mkErr := os.MkdirAll(m.pluginsDir(), 0o755); mkErr != nil {
			return LockedPlugin{}, fmt.Errorf("creating plugins dir: %w", mkErr)
		}
		staging, err = os.MkdirTemp(m.pluginsDir(), ".staging-")
		if err != nil {
			return LockedPlugin{}, fmt.Errorf("creating staging dir: %w", err)
		}
	}
	checkout := filepath.Join(staging, "src")
	defer os.RemoveAll(staging)

	if err := m.clone(ctx, src, ref, checkout); err != nil {
		return LockedPlugin{}, err
	}

	goModModule, err := readModulePath(filepath.Join(checkout, "go.mod"))
	if err != nil {
		return LockedPlugin{}, fmt.Errorf("plugin has no valid go.mod: %w", err)
	}

	manifest, err := LoadManifest(checkout)
	if err != nil {
		return LockedPlugin{}, err
	}
	if err := manifest.Validate(goModModule, m.coreVersion); err != nil {
		return LockedPlugin{}, err
	}

	commit, err := m.headCommit(ctx, checkout)
	if err != nil {
		return LockedPlugin{}, err
	}

	dest := filepath.Join(m.pluginsDir(), manifest.Name)
	if _, statErr := os.Stat(dest); statErr == nil {
		if !force {
			return LockedPlugin{}, fmt.Errorf("plugin %q is %w — pass --force to replace", manifest.Name, ErrAlreadyInstalled)
		}
		if err := os.RemoveAll(dest); err != nil {
			return LockedPlugin{}, fmt.Errorf("removing existing plugin dir: %w", err)
		}
	}

	if err := stripGitDir(checkout); err != nil {
		return LockedPlugin{}, fmt.Errorf("stripping .git: %w", err)
	}
	if err := os.Rename(checkout, dest); err != nil {
		return LockedPlugin{}, fmt.Errorf("moving plugin into place: %w", err)
	}

	locked := LockedPlugin{
		Name:       manifest.Name,
		Module:     manifest.Module,
		Version:    manifest.Version,
		Import:     manifest.ImportPath(),
		Dir:        filepath.ToSlash(filepath.Join(PluginsDirName, manifest.Name)),
		Source:     src.CloneURL,
		Declared:   strings.TrimSpace(rawSource),
		Ref:        ref,
		Commit:     commit,
		Entrypoint: manifest.EntrypointDir(),
	}

	lf, err := LoadLockFile(m.lockPath())
	if err != nil {
		return LockedPlugin{}, err
	}
	lf.Upsert(locked)

	if err := m.regenerate(ctx, lf); err != nil {
		return LockedPlugin{}, err
	}
	if err := lf.Save(m.lockPath()); err != nil {
		return LockedPlugin{}, err
	}
	return locked, nil
}

func (m *Manager) Remove(ctx context.Context, name string) (LockedPlugin, error) {
	lf, err := LoadLockFile(m.lockPath())
	if err != nil {
		return LockedPlugin{}, err
	}
	locked, ok := lf.Find(name)
	if !ok {
		return LockedPlugin{}, fmt.Errorf("plugin %q is %w", name, ErrNotInstalled)
	}

	lf.Remove(name)

	if _, err := m.run(ctx, m.moduleRoot, "go", "mod", "edit",
		"-dropreplace="+locked.Module, "-droprequire="+locked.Module); err != nil {
		return LockedPlugin{}, fmt.Errorf("editing go.mod: %w", err)
	}
	if err := os.RemoveAll(filepath.Join(m.moduleRoot, filepath.FromSlash(locked.Dir))); err != nil {
		return LockedPlugin{}, fmt.Errorf("deleting plugin dir: %w", err)
	}
	if err := m.regenerate(ctx, lf); err != nil {
		return LockedPlugin{}, err
	}
	if err := lf.Save(m.lockPath()); err != nil {
		return LockedPlugin{}, err
	}
	return locked, nil
}

func (m *Manager) List() ([]LockedPlugin, error) {
	lf, err := LoadLockFile(m.lockPath())
	if err != nil {
		return nil, err
	}
	return lf.Plugins, nil
}

// ModeFileName records how this checkout is deployed ("local" or "docker"),
// written by install.sh so `plugin install` can apply changes the right way.
const ModeFileName = ".beelzebub-mode"

// installMode returns the recorded deploy mode, defaulting to "local".
func (m *Manager) installMode() string {
	raw, err := os.ReadFile(filepath.Join(m.moduleRoot, ModeFileName))
	if err != nil {
		return "local"
	}
	if strings.TrimSpace(string(raw)) == "docker" {
		return "docker"
	}
	return "local"
}

// Build compiles the beelzebub binary so freshly installed plugins are compiled
// in. It is the same `go build` the Makefile runs (without version stamping).
func (m *Manager) Build(ctx context.Context) error {
	if _, err := m.run(ctx, m.moduleRoot, "go", "build", "-o", "beelzebub", "."); err != nil {
		return fmt.Errorf("go build: %w", err)
	}
	return nil
}

// Apply makes freshly installed plugins take effect in the current deployment:
// a local `go build`, or a Docker image rebuild + container restart. Returns the
// mode used ("local" or "docker").
func (m *Manager) Apply(ctx context.Context) (string, error) {
	if m.installMode() == "docker" {
		name, pre := "docker", []string{"compose"}
		if _, err := m.run(ctx, m.moduleRoot, "docker", "compose", "version"); err != nil {
			name, pre = "docker-compose", nil // fall back to Compose v1
		}
		args := append(pre, "up", "-d", "--build")
		if _, err := m.run(ctx, m.moduleRoot, name, args...); err != nil {
			return "docker", fmt.Errorf("docker compose up: %w", err)
		}
		return "docker", nil
	}
	if err := m.Build(ctx); err != nil {
		return "local", err
	}
	return "local", nil
}

func (m *Manager) regenerate(ctx context.Context, lf *LockFile) error {
	if err := writeImportsFile(m.pluginsDir(), lf); err != nil {
		return err
	}
	for _, p := range lf.Plugins {
		replace := fmt.Sprintf("-replace=%s=./%s", p.Module, p.Dir)
		if _, err := m.run(ctx, m.moduleRoot, "go", "mod", "edit", replace); err != nil {
			return fmt.Errorf("adding go.mod replace for %q: %w", p.Name, err)
		}
	}
	if _, err := m.run(ctx, m.moduleRoot, "go", "mod", "tidy"); err != nil {
		return fmt.Errorf("go mod tidy: %w", err)
	}
	return nil
}

func findModuleRoot(start string) (string, error) {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found above %s (run inside the beelzebub source tree)", start)
		}
		dir = parent
	}
}

func readModulePath(goModPath string) (string, error) {
	raw, err := os.ReadFile(goModPath)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module")), nil
		}
	}
	return "", fmt.Errorf("no module directive in %s", goModPath)
}
