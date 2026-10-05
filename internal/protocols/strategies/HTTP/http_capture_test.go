package HTTP

import (
	"errors"
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
		assert.Equal(t, "7", m["body.request.size"])
		assert.Equal(t, "false", m["body.request.truncated"])
		assert.Equal(t, "201", m["http.response.status"])
		assert.Equal(t, "11", m["body.response.size"])
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
		assert.Equal(t, "1048576", m["body.request.size"])
		assert.Equal(t, "true", m["body.request.truncated"])
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
		assert.Equal(t, "405", m["http.response.status"])
		assert.Equal(t, "0", m["body.request.size"])
		assert.Equal(t, "false", m["body.request.truncated"])
	}
}

type partialErrorReader struct{ done bool }

func (r *partialErrorReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, errors.New("body read failed")
	}
	r.done = true
	return copy(p, "partial"), errors.New("body read failed")
}

func TestCaptureDescriptors_ReadErrorIsNotReportedAsComplete(t *testing.T) {
	tr := &captureTracer{}
	conf := captureConf()
	req := httptest.NewRequest("POST", "/api", &partialErrorReader{})
	newHTTPHandler(conf, tr)(httptest.NewRecorder(), req)

	if assert.Len(t, tr.events, 1) {
		m := tr.events[0].Metadata
		assert.Equal(t, "7", m["body.request.size"])
		assert.Equal(t, "false", m["body.request.truncated"])
		assert.Equal(t, "true", m["body.request.read_error"])
		assert.Equal(t, "partial", tr.events[0].Body)
	}
}
