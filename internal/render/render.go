// Package render substitutes a small, fixed set of request-time tokens into
// static honeypot responses so that repeated requests are not byte-identical.
//
// It is deliberately not a template engine: one pass, a closed token set, no
// access to the request body, environment, or filesystem, and attacker input
// is never rendered. Unknown tokens are left untouched.
package render

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MaxOutput bounds the rendered result; tokens cannot grow a body past it.
const MaxOutput = 1 << 20

var tokenPattern = regexp.MustCompile(`\{\{\s*([a-z0-9_.]+)\s*\}\}`)

// Values are the per-request inputs shared by every token in one response.
type Values struct {
	RequestID string
	Now       time.Time
}

// NewValues returns fresh values for one request.
func NewValues() Values {
	return Values{RequestID: uuid.New().String(), Now: time.Now().UTC()}
}

// Render substitutes tokens in s. Supported tokens:
//
//	{{uuid.v4}}      a new UUID at every occurrence
//	{{uuid.short}}   the first 8 hex characters of a new UUID
//	{{request.id}}   one UUID stable across the whole response
//	{{now.unix}}     seconds since the epoch
//	{{now.rfc3339}}  RFC 3339 UTC timestamp
//
// Every value is UUID text, hex, digits, or an RFC 3339 timestamp, so the
// output is safe inside JSON strings and header values without escaping.
func Render(s string, v Values) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	out := tokenPattern.ReplaceAllStringFunc(s, func(match string) string {
		name := tokenPattern.FindStringSubmatch(match)[1]
		switch name {
		case "uuid.v4":
			return uuid.New().String()
		case "uuid.short":
			return uuid.New().String()[:8]
		case "request.id":
			return v.RequestID
		case "now.unix":
			return strconv.FormatInt(v.Now.Unix(), 10)
		case "now.rfc3339":
			return v.Now.Format(time.RFC3339)
		}
		return match
	})
	if len(out) > MaxOutput {
		out = out[:MaxOutput]
	}
	return out
}

// RenderAll renders every string in the slice with the same values.
func RenderAll(items []string, v Values) []string {
	if len(items) == 0 {
		return items
	}
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = Render(item, v)
	}
	return out
}
