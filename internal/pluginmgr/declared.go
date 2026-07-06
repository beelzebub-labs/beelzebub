package pluginmgr

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DeclaredFileName is the human-edited list of plugins to compile into the build
const DeclaredFileName = "beelzebub.plugins"

func (m *Manager) declaredPath() string { return filepath.Join(m.moduleRoot, DeclaredFileName) }

// declaredSources returns the plugin sources listed in beelzebub.plugins.
// A missing file is treated as an empty list.
func (m *Manager) declaredSources() ([]string, error) {
	f, err := os.Open(m.declaredPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", DeclaredFileName, err)
	}
	defer f.Close()

	var sources []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sources = append(sources, line)
	}
	return sources, sc.Err()
}

// Declare appends a source to beelzebub.plugins if it is not already listed, so
// the file always reflects what has been installed via the CLI.
func (m *Manager) Declare(source string) error {
	existing, err := m.declaredSources()
	if err != nil {
		return err
	}
	for _, s := range existing {
		if s == source {
			return nil
		}
	}
	f, err := os.OpenFile(m.declaredPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("updating %s: %w", DeclaredFileName, err)
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, source)
	return err
}

// UpdateResult reports what happened to one plugin during an update.
type UpdateResult struct {
	Name      string
	OldCommit string
	NewCommit string
	Changed   bool
}

// lockByURL indexes installed plugins by their clone URL.
func (m *Manager) lockByURL() (map[string]LockedPlugin, error) {
	lf, err := LoadLockFile(m.lockPath())
	if err != nil {
		return nil, err
	}
	byURL := make(map[string]LockedPlugin, len(lf.Plugins))
	for _, p := range lf.Plugins {
		byURL[p.Source] = p
	}
	return byURL, nil
}

// present reports whether the plugin's vendored source directory exists.
func (m *Manager) present(p LockedPlugin) bool {
	_, err := os.Stat(filepath.Join(m.moduleRoot, filepath.FromSlash(p.Dir)))
	return err == nil
}

// InstallDeclared installs every plugin listed in beelzebub.plugins.
func (m *Manager) InstallDeclared(ctx context.Context, force bool) ([]LockedPlugin, error) {
	sources, err := m.declaredSources()
	if err != nil {
		return nil, err
	}
	lf, err := LoadLockFile(m.lockPath())
	if err != nil {
		return nil, err
	}
	byDeclared := make(map[string]LockedPlugin, len(lf.Plugins))
	byURL := make(map[string]LockedPlugin, len(lf.Plugins))
	for _, p := range lf.Plugins {
		byURL[p.Source] = p
		if p.Declared != "" {
			byDeclared[p.Declared] = p
		}
	}

	var installed []LockedPlugin
	for _, src := range sources {
		// Already installed at this exact declaration and present on disk: skip
		if locked, ok := byDeclared[src]; ok && !force && m.present(locked) {
			continue
		}
		parsed, perr := ParseSource(src)
		if perr != nil {
			return installed, fmt.Errorf("%s: %w", DeclaredFileName, perr)
		}
		locked, exists := byURL[parsed.CloneURL]
		if exists && !force && locked.Ref == parsed.Ref && m.present(locked) {
			continue // already installed at the requested ref
		}
		newLocked, err := m.Install(ctx, src, parsed.Ref, force || exists)
		if err != nil {
			return installed, fmt.Errorf("installing %q from %s: %w", src, DeclaredFileName, err)
		}
		installed = append(installed, newLocked)
	}
	return installed, nil
}

// Update re-fetches installed plugins at the ref declared in beelzebub.plugins
// and re-pins the resolved commit
func (m *Manager) Update(ctx context.Context, name string) ([]UpdateResult, error) {
	sources, err := m.declaredSources()
	if err != nil {
		return nil, err
	}
	byURL, err := m.lockByURL()
	if err != nil {
		return nil, err
	}

	var results []UpdateResult
	matched := false
	for _, src := range sources {
		parsed, perr := ParseSource(src)
		if perr != nil {
			return results, fmt.Errorf("%s: %w", DeclaredFileName, perr)
		}
		locked, exists := byURL[parsed.CloneURL]
		if !exists {
			continue // declared but not installed — `install` handles those
		}
		if name != "" && locked.Name != name {
			continue
		}
		matched = true
		newLocked, err := m.Install(ctx, src, parsed.Ref, true)
		if err != nil {
			return results, fmt.Errorf("updating %q: %w", locked.Name, err)
		}
		results = append(results, UpdateResult{
			Name:      locked.Name,
			OldCommit: locked.Commit,
			NewCommit: newLocked.Commit,
			Changed:   locked.Commit != newLocked.Commit,
		})
	}
	if name != "" && !matched {
		return results, fmt.Errorf("plugin %q is %w", name, ErrNotInstalled)
	}
	return results, nil
}
