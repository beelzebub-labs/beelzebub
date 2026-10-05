package render

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestRenderSubstitutesKnownTokens(t *testing.T) {
	v := Values{RequestID: "req-1", Now: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)}
	out := Render(`{"id":"{{request.id}}","t":{{now.unix}},"at":"{{ now.rfc3339 }}","u":"{{uuid.v4}}","s":"{{uuid.short}}"}`, v)
	assert.Contains(t, out, `"id":"req-1"`)
	assert.Contains(t, out, `"t":`+strconv.FormatInt(v.Now.Unix(), 10))
	assert.Contains(t, out, `"at":"2026-09-04T12:00:00Z"`)
	assert.NotContains(t, out, "{{")
}

func TestRenderLeavesUnknownTokensAndPlainText(t *testing.T) {
	v := NewValues()
	assert.Equal(t, "no tokens here", Render("no tokens here", v))
	assert.Equal(t, "{{secret.key}} {{env.HOME}}", Render("{{secret.key}} {{env.HOME}}", v))
}

func TestRenderIsPerRequestAndPerOccurrence(t *testing.T) {
	a := Render("{{request.id}}|{{request.id}}|{{uuid.v4}}|{{uuid.v4}}", NewValues())
	b := Render("{{request.id}}", NewValues())
	parts := strings.Split(a, "|")
	assert.Equal(t, parts[0], parts[1], "request.id is stable within a response")
	assert.NotEqual(t, parts[2], parts[3], "uuid.v4 is fresh per occurrence")
	assert.NotEqual(t, parts[0], b, "request.id differs across requests")
	_, err := uuid.Parse(parts[2])
	assert.NoError(t, err)
}

func TestRenderIsBounded(t *testing.T) {
	huge := strings.Repeat("{{uuid.v4}}", MaxOutput/8)
	assert.LessOrEqual(t, len(Render(huge, NewValues())), MaxOutput)
}
