package HTTP

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/stretchr/testify/assert"
)

type captureTracer struct{ events []tracer.Event }

func (c *captureTracer) TraceEvent(e tracer.Event) { c.events = append(c.events, e) }

func captureConf() parser.BeelzebubServiceConfiguration {
	conf := parser.BeelzebubServiceConfiguration{
		Protocol:    "http",
		Address:     ":8080",
		Description: "capture test",
		Commands: []parser.Command{{
			RegexStr:   "^/api$",
			Methods:    []string{"POST"},
			Handler:    `{"ok":true}`,
			StatusCode: 201,
		}},
	}
	_ = conf.CompileCommandRegex()
	return conf
}

func TestCaptureDescriptors_SmallBodyIsComplete(t *testing.T) {
	tr := &captureTracer{}
	conf := captureConf()
	req := httptest.NewRequest("POST", "/api", strings.NewReader(`{"a":1}`))
	rec := httptest.NewRecorder()
	newHTTPHandler(conf, tr)(rec, req)

	if assert.Len(t, tr.events, 1) {
		m := tr.events[0].Metadata
		assert.Equal(t, "7", m["capture.request.bytes"])
		assert.Equal(t, "false", m["capture.request.truncated"])
		assert.Equal(t, "201", m["capture.response.status"])
		assert.Equal(t, "11", m["capture.response.bytes"])
		assert.Equal(t, `{"a":1}`, tr.events[0].Body)
	}
	assert.Equal(t, 201, rec.Code)
}

func TestCaptureDescriptors_OversizeBodyIsMarkedTruncated(t *testing.T) {
	tr := &captureTracer{}
	conf := captureConf()
	big := strings.Repeat("x", maxRequestBodyBytes+512)
	req := httptest.NewRequest("POST", "/api", strings.NewReader(big))
	newHTTPHandler(conf, tr)(httptest.NewRecorder(), req)

	if assert.Len(t, tr.events, 1) {
		m := tr.events[0].Metadata
		assert.Equal(t, "1048576", m["capture.request.bytes"])
		assert.Equal(t, "true", m["capture.request.truncated"])
		assert.Len(t, tr.events[0].Body, maxRequestBodyBytes)
	}
}

func TestCaptureDescriptors_MethodNotAllowedPathIsDescribed(t *testing.T) {
	tr := &captureTracer{}
	conf := captureConf()
	req := httptest.NewRequest("GET", "/api", nil)
	rec := httptest.NewRecorder()
	newHTTPHandler(conf, tr)(rec, req)

	assert.Equal(t, 405, rec.Code)
	if assert.Len(t, tr.events, 1) {
		m := tr.events[0].Metadata
		assert.Equal(t, "405", m["capture.response.status"])
		assert.Equal(t, "0", m["capture.request.bytes"])
		assert.Equal(t, "false", m["capture.request.truncated"])
	}
}
