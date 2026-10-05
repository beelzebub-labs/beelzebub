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
	if len(s) > MaxOutput {
		return s
	}

	// Render into a bounded builder. If token expansion would cross the limit,
	// return the configured response unchanged; truncating a rendered body can
	// turn valid JSON or another framed payload into a malformed response.
	var out strings.Builder
	out.Grow(len(s))
	cursor := 0
	for cursor < len(s) {
		match := tokenPattern.FindStringSubmatchIndex(s[cursor:])
		if match == nil {
			break
		}
		start := cursor + match[0]
		end := cursor + match[1]
		name := s[cursor+match[2] : cursor+match[3]]
		replacement := name
		switch name {
		case "uuid.v4":
			replacement = uuid.New().String()
		case "uuid.short":
			replacement = uuid.New().String()[:8]
		case "request.id":
			replacement = v.RequestID
		case "now.unix":
			replacement = strconv.FormatInt(v.Now.Unix(), 10)
		case "now.rfc3339":
			replacement = v.Now.Format(time.RFC3339)
		default:
			replacement = s[start:end]
		}

		segmentSize := start - cursor + len(replacement)
		if out.Len()+segmentSize > MaxOutput {
			return s
		}
		out.WriteString(s[cursor:start])
		out.WriteString(replacement)
		cursor = end
	}
	if out.Len()+len(s)-cursor > MaxOutput {
		return s
	}
	out.WriteString(s[cursor:])
	return out.String()
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
