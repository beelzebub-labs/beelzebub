package pluginmgr

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
