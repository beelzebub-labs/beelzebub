package pluginmgr

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Runner func(ctx context.Context, dir, name string, args ...string) (string, error)

func execRunner(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s",
			name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// run git via runnuer, redact token on error
func (m *Manager) git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := m.run(ctx, dir, "git", args...)
	if err != nil {
		return out, fmt.Errorf("%s", redact(err.Error(), m.token))
	}
	return out, nil
}

// clone fetches src into dst
func (m *Manager) clone(ctx context.Context, src RepoSource, ref, dst string) error {
	url := src.CloneURL
	if !src.SSH && !src.Local && m.token != "" {
		url = injectToken(src, m.token)
	}

	if _, err := m.git(ctx, "", "clone", "--depth", "1", url, dst); err != nil {
		return err
	}

	if ref != "" {
		if _, err := m.git(ctx, dst, "fetch", "--depth", "1", "origin", ref); err != nil {
			if _, ferr := m.git(ctx, dst, "fetch", "--unshallow"); ferr != nil {
				return fmt.Errorf("fetching ref %q: %w", ref, err)
			}
		}
		if _, err := m.git(ctx, dst, "checkout", "--detach", ref); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) headCommit(ctx context.Context, dir string) (string, error) {
	out, err := m.git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func stripGitDir(pluginDir string) error {
	return os.RemoveAll(filepath.Join(pluginDir, ".git"))
}

func injectToken(src RepoSource, token string) string {
	if token == "" || !isGitHubHost(src.Host) {
		return src.CloneURL
	}

	u, err := url.Parse(src.CloneURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return src.CloneURL
	}

	u.User = url.UserPassword("x-access-token", token)
	return u.String()
}

func isGitHubHost(host string) bool {
	return strings.EqualFold(host, "github.com")
}

func redact(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "***")
}
