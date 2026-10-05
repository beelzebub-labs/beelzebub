package HTTP

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/stretchr/testify/assert"
)

type dynTracer struct{}

func (dynTracer) TraceEvent(tracer.Event) {}

func dynamicConf(enabled bool) parser.BeelzebubServiceConfiguration {
	conf := parser.BeelzebubServiceConfiguration{
		Protocol: "http", Address: ":8080", Description: "dynamic test",
		Commands: []parser.Command{{
			RegexStr:   "^/status$",
			Handler:    `{"node":"{{uuid.short}}","request":"{{request.id}}"}`,
			Headers:    []string{"X-Request-Id: {{request.id}}"},
			StatusCode: 200,
		}},
	}
	if enabled {
		conf.Dynamic = &parser.DynamicResponses{Enabled: true}
	}
	_ = conf.CompileCommandRegex()
	return conf
}

func TestDynamicDisabledLeavesHandlerVerbatim(t *testing.T) {
	rec := httptest.NewRecorder()
	newHTTPHandler(dynamicConf(false), dynTracer{})(rec, httptest.NewRequest("GET", "/status", nil))
	assert.Equal(t, `{"node":"{{uuid.short}}","request":"{{request.id}}"}`, rec.Body.String())
}

func TestDynamicEnabledVariesPerRequest(t *testing.T) {
	h := newHTTPHandler(dynamicConf(true), dynTracer{})
	r1, r2 := httptest.NewRecorder(), httptest.NewRecorder()
	h(r1, httptest.NewRequest("GET", "/status", nil))
	h(r2, httptest.NewRequest("GET", "/status", nil))
	assert.NotContains(t, r1.Body.String(), "{{")
	assert.NotEqual(t, r1.Body.String(), r2.Body.String())
	id1 := strings.TrimSpace(r1.Header().Get("X-Request-Id"))
	id2 := strings.TrimSpace(r2.Header().Get("X-Request-Id"))
	assert.NotEqual(t, id1, id2)
	assert.Contains(t, r1.Body.String(), id1, "request.id is shared by body and header")
}
