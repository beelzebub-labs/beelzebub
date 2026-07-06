package pluginmgr

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClone_RealGit drives the real git path (clone, checkout, rev-parse, strip)
// against a local repo, so no network is needed. Skips if git is unavailable.
func TestClone_RealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()

	// Build a local "plugin" repo with one commit.
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module github.com/acme/scanner\n\ngo 1.25\n")
	writeFile(t, repo, ManifestFile, validManifest)
	gitInit(t, repo)

	m := &Manager{moduleRoot: t.TempDir(), run: execRunner}
	dst := filepath.Join(t.TempDir(), "checkout")

	require.NoError(t, m.clone(ctx, RepoSource{CloneURL: repo}, "", dst))

	commit, err := m.headCommit(ctx, dst)
	require.NoError(t, err)
	assert.Len(t, commit, 40, "rev-parse should return a full SHA")

	require.NoError(t, stripGitDir(dst))
	assert.NoDirExists(t, filepath.Join(dst, ".git"))
	assert.FileExists(t, filepath.Join(dst, ManifestFile))
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	for _, args := range [][]string{
		{"init", "-q"}, {"add", "-A"}, {"commit", "-qm", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
}
