package pluginmgr

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// required manifest a plugin repository must ship at its root.
const ManifestFile = "beelzebub.plugin.yaml"

type Manifest struct {
	Name           string `yaml:"name"`
	Version        string `yaml:"version"`
	Module         string `yaml:"module"`
	Entrypoint     string `yaml:"entrypoint"`
	Description    string `yaml:"description"`
	Author         string `yaml:"author"`
	MinCoreVersion string `yaml:"min-core-version"`
}

var (
	nameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	semverRe  = regexp.MustCompile(`^v?\d+\.\d+\.\d+([.\-+].*)?$`)
	versionRe = regexp.MustCompile(`\d+`)
)

func LoadManifest(pluginDir string) (Manifest, error) {
	var m Manifest
	raw, err := os.ReadFile(filepath.Join(pluginDir, ManifestFile))
	if err != nil {
		if os.IsNotExist(err) {
			return m, fmt.Errorf("missing %s at plugin root: every plugin must ship one", ManifestFile)
		}
		return m, fmt.Errorf("reading %s: %w", ManifestFile, err)
	}
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("parsing %s: %w", ManifestFile, err)
	}
	m.Name = strings.TrimSpace(m.Name)
	m.Version = strings.TrimSpace(m.Version)
	m.Module = strings.TrimSpace(m.Module)
	m.Entrypoint = strings.TrimSpace(m.Entrypoint)
	m.MinCoreVersion = strings.TrimSpace(m.MinCoreVersion)
	return m, nil
}

func (m Manifest) Validate(goModModule, coreVersion string) error {
	if m.Name == "" {
		return fmt.Errorf("%s: name is required", ManifestFile)
	}
	if !nameRe.MatchString(m.Name) {
		return fmt.Errorf("%s: name %q is invalid (must match %s)", ManifestFile, m.Name, nameRe.String())
	}
	if m.Version == "" {
		return fmt.Errorf("%s: version is required", ManifestFile)
	}
	if !semverRe.MatchString(m.Version) {
		return fmt.Errorf("%s: version %q is not semver (e.g. 1.2.0)", ManifestFile, m.Version)
	}
	if m.Module == "" {
		return fmt.Errorf("%s: module is required", ManifestFile)
	}
	if goModModule != "" && m.Module != goModModule {
		return fmt.Errorf("%s: module %q does not match go.mod module %q", ManifestFile, m.Module, goModModule)
	}
	if err := m.checkMinCoreVersion(coreVersion); err != nil {
		return err
	}
	return nil
}

func (m Manifest) checkMinCoreVersion(coreVersion string) error {
	if m.MinCoreVersion == "" {
		return nil
	}
	if coreVersion == "" || coreVersion == "dev" || coreVersion == "unknown" {
		return nil
	}
	if compareVersions(coreVersion, m.MinCoreVersion) < 0 {
		return fmt.Errorf("%s: requires core %s or newer, but this core is %s (rebuild against a newer core)",
			ManifestFile, m.MinCoreVersion, coreVersion)
	}
	return nil
}

func (m Manifest) ImportPath() string {
	ep := m.Entrypoint
	if ep == "" || ep == "." {
		return m.Module
	}
	ep = strings.TrimPrefix(filepath.ToSlash(ep), "./")
	return path.Join(m.Module, ep)
}

func (m Manifest) EntrypointDir() string {
	if m.Entrypoint == "" {
		return "."
	}
	return filepath.Clean(m.Entrypoint)
}

func compareVersions(a, b string) int {
	pa, pb := parseVersionParts(a), parseVersionParts(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func parseVersionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	// Drop pre-release/build metadata before splitting on dots.
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	fields := strings.Split(v, ".")
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		m := versionRe.FindString(f)
		n, _ := strconv.Atoi(m)
		out = append(out, n)
	}
	return out
}
