package pluginmgr

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInjectToken(t *testing.T) {
	got := injectToken(RepoSource{CloneURL: "https://github.com/acme/foo.git", Host: "github.com"}, "secret123")
	assert.Equal(t, "https://x-access-token:secret123@github.com/acme/foo.git", got)

	assert.Equal(t, "git@github.com:acme/foo.git",
		injectToken(RepoSource{CloneURL: "git@github.com:acme/foo.git", Host: "github.com"}, "secret123"))

	assert.Equal(t, "https://gitlab.com/acme/foo.git",
		injectToken(RepoSource{CloneURL: "https://gitlab.com/acme/foo.git", Host: "gitlab.com"}, "secret123"))

	assert.Equal(t, "https://github.com/acme/foo.git",
		injectToken(RepoSource{CloneURL: "https://github.com/acme/foo.git", Host: "github.com"}, ""))

	assert.Equal(t, "://bad-url",
		injectToken(RepoSource{CloneURL: "://bad-url", Host: "github.com"}, "secret123"))
}

func TestRedact(t *testing.T) {
	assert.Equal(t, "clone failed for ***", redact("clone failed for secret123", "secret123"))
	assert.Equal(t, "no token here", redact("no token here", ""))
}

func TestClone_FetchFallbackAndRedactsToken(t *testing.T) {
	var calls []string
	m := &Manager{
		token: "secret123",
		run: func(_ context.Context, _ string, name string, args ...string) (string, error) {
			call := name + " " + fmt.Sprint(args)
			calls = append(calls, call)

			switch {
			case name == "git" && len(args) >= 1 && args[0] == "clone":
				assert.Contains(t, args[3], "x-access-token:secret123")
				return "", nil

			case name == "git" && len(args) >= 4 && args[0] == "fetch" && args[1] == "--depth":
				return "", fmt.Errorf("fetch failed with secret123")

			case name == "git" && len(args) >= 2 && args[0] == "fetch" && args[1] == "--unshallow":
				return "", nil

			case name == "git" && len(args) >= 1 && args[0] == "checkout":
				return "", nil

			default:
				return "", fmt.Errorf("unexpected call %s", call)
			}
		},
	}

	err := m.clone(context.Background(), RepoSource{
		CloneURL: "https://github.com/acme/foo.git",
		Host:     "github.com",
	}, "main", t.TempDir())

	require.NoError(t, err)
	assert.Contains(t, fmt.Sprint(calls), "--unshallow")

	m.run = func(context.Context, string, string, ...string) (string, error) {
		return "", fmt.Errorf("clone failed with secret123")
	}

	err = m.clone(context.Background(), RepoSource{
		CloneURL: "https://github.com/acme/foo.git",
		Host:     "github.com",
	}, "", t.TempDir())

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret123")
	assert.Contains(t, err.Error(), "***")
}

func TestHeadCommit_Error(t *testing.T) {
	m := &Manager{run: func(context.Context, string, string, ...string) (string, error) {
		return "", fmt.Errorf("rev-parse failed")
	}}

	_, err := m.headCommit(context.Background(), t.TempDir())
	require.Error(t, err)
}
