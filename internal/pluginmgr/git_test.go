package pluginmgr

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInjectToken(t *testing.T) {
	got := injectToken("https://github.com/acme/foo.git", "secret123")
	assert.Equal(t, "https://x-access-token:secret123@github.com/acme/foo.git", got)
	assert.Equal(t, "git@github.com:acme/foo.git", injectToken("git@github.com:acme/foo.git", "secret123"))
}

func TestRedact(t *testing.T) {
	assert.Equal(t, "clone failed for ***", redact("clone failed for secret123", "secret123"))
	assert.Equal(t, "no token here", redact("no token here", ""))
}
