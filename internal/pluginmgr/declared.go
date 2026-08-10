package pluginmgr

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DeclaredFileName is the human-edited deployment plugin list.
const DeclaredFileName = "plugins.yaml"

const ConfigurationsDirName = "configurations"

const DeclaredConfigPath = ConfigurationsDirName + "/" + DeclaredFileName

const DeclaredConfigDir = ConfigurationsDirName + "/plugins"

const declaredHeader = `# Plugins to compile into this deployment.
#
# Add a plugin by writing its repository here, or run:
#     beelzebub plugin install <github-link>
# which appends it to this file for you.
#
# Future per-plugin runtime configuration can live under configurations/plugins/
# as one YAML file per plugin.
`

type declaredFile struct {
	Plugins []declaredPlugin `yaml:"plugins"`
}

type declaredPlugin struct {
	Source string `yaml:"source"`
}

func (m *Manager) declaredPath() string {
	return filepath.Join(m.moduleRoot, ConfigurationsDirName, DeclaredFileName)
}

// declaredSources returns the plugin sources listed in configurations/plugins.yaml.
// A missing file is treated as an empty list.
func (m *Manager) declaredSources() ([]string, error) {
	df, err := m.loadDeclaredFile()

	if err != nil {
		return nil, err
	}

	sources := make([]string, 0, len(df.Plugins))
	for i, p := range df.Plugins {
		source := strings.TrimSpace(p.Source)

		if source == "" {
			return nil, fmt.Errorf("%s: plugins[%d].source is required", DeclaredConfigPath, i)
		}

		sources = append(sources, source)
	}

	return sources, nil
}

func (m *Manager) loadDeclaredFile() (*declaredFile, error) {
	df := &declaredFile{}
	raw, err := os.ReadFile(m.declaredPath())

	if err != nil {
		if os.IsNotExist(err) {
			return df, nil
		}

		return nil, fmt.Errorf("reading %s: %w", m.declaredPath(), err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)

	if err := dec.Decode(df); err != nil {
		if err == io.EOF {
			return df, nil
		}

		return nil, fmt.Errorf("parsing %s: %w", m.declaredPath(), err)
	}

	return df, nil
}

func (m *Manager) saveDeclaredFile(df *declaredFile) error {
	for i := range df.Plugins {
		df.Plugins[i].Source = strings.TrimSpace(df.Plugins[i].Source)
	}

	raw, err := yaml.Marshal(df)
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", DeclaredFileName, err)
	}

	if err := os.MkdirAll(filepath.Dir(m.declaredPath()), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(m.declaredPath()), err)
	}

	raw = append([]byte(declaredHeader), raw...)
	if err := os.WriteFile(m.declaredPath(), raw, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", m.declaredPath(), err)
	}

	return nil
}

// Declare adds a source to configurations/plugins.yaml if it is not already
// listed, so the file reflects what has been installed via the CLI.
func (m *Manager) Declare(source string) error {
	df, err := m.loadDeclaredFile()
	if err != nil {
		return err
	}

	source = strings.TrimSpace(source)
	if source == "" {
		return fmt.Errorf("%s: plugin source is required", DeclaredConfigPath)
	}

	for _, p := range df.Plugins {
		if strings.TrimSpace(p.Source) == source {
			return nil
		}
	}

	df.Plugins = append(df.Plugins, declaredPlugin{Source: source})
	return m.saveDeclaredFile(df)
}

// Undeclare removes a plugin from configurations/plugins.yaml.
func (m *Manager) Undeclare(plugin LockedPlugin) error {
	df, err := m.loadDeclaredFile()
	if err != nil {
		return err
	}

	out := make([]declaredPlugin, 0, len(df.Plugins))
	changed := false

	for _, p := range df.Plugins {
		if declarationMatchesPlugin(strings.TrimSpace(p.Source), plugin) {
			changed = true
			continue
		}

		out = append(out, p)
	}

	if !changed {
		return nil
	}

	df.Plugins = out
	return m.saveDeclaredFile(df)
}

func declarationMatchesPlugin(line string, plugin LockedPlugin) bool {
	if plugin.Declared != "" && line == plugin.Declared {
		return true
	}

	src, err := ParseSource(line)
	if err != nil {
		return false
	}

	return src.CloneURL == plugin.Source
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

// InstallDeclared installs every plugin listed in configurations/plugins.yaml.
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
			return installed, fmt.Errorf("%s: %w", DeclaredConfigPath, perr)
		}
		locked, exists := byURL[parsed.CloneURL]
		if exists && !force && locked.Ref == parsed.Ref && m.present(locked) {
			continue // already installed at the requested ref
		}
		newLocked, err := m.Install(ctx, src, parsed.Ref, force || exists)
		if err != nil {
			return installed, fmt.Errorf("installing %q from %s: %w", src, DeclaredConfigPath, err)
		}
		installed = append(installed, newLocked)
	}
	return installed, nil
}

// Update re-fetches installed plugins at the ref declared in configurations/plugins.yaml.
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
			return results, fmt.Errorf("%s: %w", DeclaredConfigPath, perr)
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
