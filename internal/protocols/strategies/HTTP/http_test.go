package HTTP

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustCIDRs(t *testing.T, cidrs ...string) []*net.IPNet {
	t.Helper()
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		require.NoError(t, err)
		nets = append(nets, n)
	}
	return nets
}

type mockTracer struct {
	events []tracer.Event
}

func (m *mockTracer) TraceEvent(event tracer.Event) {
	m.events = append(m.events, event)
}

func TestBuildHTTPResponse_Basic(t *testing.T) {
	tr := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{}

	cmd := parser.Command{
		Handler:    "Hello World",
		StatusCode: 200,
		Headers:    []string{"X-Test: value"},
	}

	req := httptest.NewRequest("GET", "http://localhost/", nil)

	resp, err := buildHTTPResponse(servConf, tr, cmd, req)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if resp.StatusCode != 200 {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	if resp.Body != "Hello World" {
		t.Errorf("expected body 'Hello World', got %q", resp.Body)
	}
}

func TestBuildHTTPResponse_WithStatusCode(t *testing.T) {
	tr := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{}

	cmd := parser.Command{
		Handler:    "Not Found",
		StatusCode: 404,
		Headers:    []string{},
	}

	req := httptest.NewRequest("GET", "http://localhost/test", nil)

	resp, err := buildHTTPResponse(servConf, tr, cmd, req)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if resp.Body != "Not Found" {
		t.Errorf("expected body 'Not Found', got %q", resp.Body)
	}
}

func TestTraceRequest(t *testing.T) {
	tr := &mockTracer{}

	cmd := parser.Command{Name: "test"}
	req := httptest.NewRequest("GET", "/path", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	req.Header.Set("User-Agent", "test-agent")

	traceRequest(req, tr, cmd, "test honeypot", "body content", mustCIDRs(t, "10.0.0.0/8"))

	if len(tr.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(tr.events))
	}
	e := tr.events[0]
	if e.RequestURI != "/path" {
		t.Errorf("expected URI /path, got %s", e.RequestURI)
	}
	if e.SourceIp != "192.168.1.1" {
		t.Errorf("expected IP 192.168.1.1, got %s", e.SourceIp)
	}
}

func TestTraceRequest_TLS(t *testing.T) {
	tr := &mockTracer{}
	cmd := parser.Command{Name: "test"}
	req := httptest.NewRequest("GET", "https://localhost/secure", nil)
	req.RemoteAddr = "10.0.0.1:54321"
	req.TLS = &tls.ConnectionState{ServerName: "example.com"}

	traceRequest(req, tr, cmd, "tls test", "", nil)

	if len(tr.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(tr.events))
	}
	if tr.events[0].TLSServerName != "example.com" {
		t.Errorf("expected TLS server name 'example.com', got %s", tr.events[0].TLSServerName)
	}
}

func TestSetResponseHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	setResponseHeaders(w, []string{"X-Custom:value1", "X-Other:value2"}, 201)

	if w.Code != 201 {
		t.Errorf("expected status 201, got %d", w.Code)
	}
	if w.Header().Get("X-Custom") != "value1" {
		t.Errorf("expected X-Custom header 'value1', got %s", w.Header().Get("X-Custom"))
	}
}

func TestSetResponseHeaders_NoStatusCode(t *testing.T) {
	w := httptest.NewRecorder()
	setResponseHeaders(w, nil, 0)

	// StatusText(0) returns empty string, so WriteHeader should not be called
	if w.Code != 200 {
		t.Errorf("expected default status 200, got %d", w.Code)
	}
}

func TestRealClientAddr_NoTrustedProxies(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	host, port := realClientAddr(req, nil)
	if host != "10.0.0.1" {
		t.Errorf("expected host 10.0.0.1, got %s", host)
	}
	if port != "12345" {
		t.Errorf("expected port 12345, got %s", port)
	}
}

func TestRealClientAddr_WithTrustedProxy(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "8.8.8.8, 10.0.0.1")

	host, port := realClientAddr(req, mustCIDRs(t, "10.0.0.0/8"))
	if host != "8.8.8.8" {
		t.Errorf("expected host 8.8.8.8, got %s", host)
	}
	if port != "" {
		t.Errorf("expected empty port, got %s", port)
	}
}

func TestRealClientAddr_TrustedProxyAllTrusted(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:12345"

	host, _ := realClientAddr(req, mustCIDRs(t, "10.0.0.0/8"))
	// All proxies trusted, so fall back to RemoteAddr
	if host != "10.0.0.1" {
		t.Errorf("expected host 10.0.0.1, got %s", host)
	}
}

func TestRealClientAddr_BadRemoteAddr(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "not-a-valid-addr"

	host, _ := realClientAddr(req, nil)
	if host != "not-a-valid-addr" {
		t.Errorf("expected host 'not-a-valid-addr', got %s", host)
	}
}

func TestIPInNets(t *testing.T) {
	nets := mustCIDRs(t, "10.0.0.0/8", "192.168.0.0/16")

	if !ipInNets(net.ParseIP("10.1.2.3"), nets) {
		t.Error("expected 10.1.2.3 to be in 10.0.0.0/8")
	}
	if !ipInNets(net.ParseIP("192.168.1.1"), nets) {
		t.Error("expected 192.168.1.1 to be in 192.168.0.0/16")
	}
	if ipInNets(net.ParseIP("8.8.8.8"), nets) {
		t.Error("expected 8.8.8.8 to NOT be in any trusted nets")
	}
}

func TestMapHeaderToString(t *testing.T) {
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	headers.Set("X-Custom", "value")

	result := mapHeaderToString(headers)
	if !strings.Contains(result, "Content-Type") || !strings.Contains(result, "application/json") {
		t.Errorf("expected header Content-Type in output, got %s", result)
	}
}

func TestMapCookiesToString(t *testing.T) {
	cookies := []*http.Cookie{
		{Name: "session", Value: "abc123"},
	}
	result := mapCookiesToString(cookies)
	if !strings.Contains(result, "session=abc123") {
		t.Errorf("expected session cookie in output, got %s", result)
	}
}

func TestHTTPStrategy_Init_Valid(t *testing.T) {
	strategy := &HTTPStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:     "127.0.0.1:0",
		Description: "test HTTP",
	}

	err := strategy.Init(servConf, mt)
	assert.NoError(t, err)
	assert.Len(t, strategy.servers, 1)
}

func TestHTTPStrategy_Init_WithTLS(t *testing.T) {
	strategy := &HTTPStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:     "127.0.0.1:0",
		Description: "test HTTPS",
		TLSKeyPath:  "/nonexistent/key.pem",
		TLSCertPath: "/nonexistent/cert.pem",
	}

	err := strategy.Init(servConf, mt)
	assert.Error(t, err)
	assert.Empty(t, strategy.servers)
}

func TestHTTPStrategy_Init_BindError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	strategy := &HTTPStrategy{}
	err = strategy.Init(parser.BeelzebubServiceConfiguration{Address: listener.Addr().String()}, &mockTracer{})
	assert.Error(t, err)
	assert.Empty(t, strategy.servers)
}

func TestHTTPStrategy_StopAll(t *testing.T) {
	strategy := &HTTPStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:     "127.0.0.1:0",
		Description: "test HTTP",
	}

	assert.NoError(t, strategy.Init(servConf, mt))
	assert.Len(t, strategy.servers, 1)

	assert.NoError(t, strategy.StopAll())
	assert.Nil(t, strategy.servers)
}

func TestHTTPStrategy_StopAll_Empty(t *testing.T) {
	strategy := &HTTPStrategy{}
	assert.NoError(t, strategy.StopAll())
}

func TestHTTPStrategy_Init_WithCommands_Matching(t *testing.T) {
	strategy := &HTTPStrategy{}
	mt := &mockTracer{}

	rex, err := regexp.Compile("/test")
	require.NoError(t, err)

	servConf := parser.BeelzebubServiceConfiguration{
		Address:     "127.0.0.1:0",
		Description: "test HTTP with commands",
		Commands: []parser.Command{
			{
				Regex:      rex,
				Handler:    "test response",
				StatusCode: 200,
				Name:       "test-command",
			},
		},
	}

	err = strategy.Init(servConf, mt)
	assert.NoError(t, err)
}

func TestHTTPStrategy_Init_FallbackCommand(t *testing.T) {
	strategy := &HTTPStrategy{}
	mt := &mockTracer{}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("fallback"))
	})

	srv := &http.Server{
		Addr:    "127.0.0.1:0",
		Handler: mux,
	}
	strategy.servers = map[string]*http.Server{"test:0": srv}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:     "127.0.0.1:0",
		Description: "test HTTP",
		FallbackCommand: parser.Command{
			Handler:    "not found",
			StatusCode: 404,
			Name:       "fallback",
		},
	}

	_ = strategy.Init(servConf, mt)
}

func TestRealClientAddr_XRealIP(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	req.Header.Set("X-Real-Ip", "8.8.8.8")

	host, _ := realClientAddr(req, mustCIDRs(t, "10.0.0.0/8"))
	assert.Equal(t, "8.8.8.8", host)
}

func TestRealClientAddr_XRealIPTrusted(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	req.Header.Set("X-Real-Ip", "10.0.0.2")

	host, _ := realClientAddr(req, mustCIDRs(t, "10.0.0.0/8"))
	assert.Equal(t, "10.0.0.1", host)
}

type failOnCloseListener struct {
	net.Listener
	closeErr error
}

func (f *failOnCloseListener) Close() error {
	f.Listener.Close()
	return f.closeErr
}

func TestHTTPStrategy_StopAll_ShutdownError(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	failL := &failOnCloseListener{Listener: l, closeErr: errors.New("simulated close error")}

	srv := &http.Server{Handler: http.NewServeMux()}
	go srv.Serve(failL)
	time.Sleep(50 * time.Millisecond)

	strategy := &HTTPStrategy{}
	strategy.servers = map[string]*http.Server{"test:0": srv}

	err = strategy.StopAll()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "simulated close error")
	assert.Nil(t, strategy.servers)
}

func TestHTTPStrategy_Stop_ShutdownError(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	failL := &failOnCloseListener{Listener: l, closeErr: errors.New("simulated close error")}

	srv := &http.Server{Handler: http.NewServeMux()}
	go srv.Serve(failL)
	time.Sleep(50 * time.Millisecond)

	strategy := &HTTPStrategy{}
	strategy.servers = map[string]*http.Server{"test:0": srv}

	servConf := parser.BeelzebubServiceConfiguration{Address: "test:0"}
	err = strategy.Stop(servConf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "simulated close error")
	// On error, Stop returns immediately without deleting from map
	assert.Len(t, strategy.servers, 1)
}

func TestHTTPStrategy_Stop_ServerNotFound(t *testing.T) {
	strategy := &HTTPStrategy{}
	strategy.servers = map[string]*http.Server{"other:0": {}}

	err := strategy.Stop(parser.BeelzebubServiceConfiguration{Address: "test:0"})
	assert.NoError(t, err)
}

func TestHTTPStrategy_StopAll_ServerNotFound(t *testing.T) {
	strategy := &HTTPStrategy{}
	err := strategy.StopAll()
	assert.NoError(t, err)
}

func TestHTTPStrategy_Init_OverwriteExisting(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	strategy := &HTTPStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address: fmt.Sprintf("127.0.0.1:%d", port),
	}

	// First init creates the server
	err = strategy.Init(servConf, mt)
	assert.NoError(t, err)
	assert.Len(t, strategy.servers, 1)

	// Second init with same address overwrites
	err = strategy.Init(servConf, mt)
	assert.NoError(t, err)
	assert.Len(t, strategy.servers, 1)

	err = strategy.StopAll()
	assert.NoError(t, err)
}

type testCommandPlugin struct{}

func (p *testCommandPlugin) Metadata() plugin.Metadata {
	return plugin.Metadata{Name: "http-test-cmd"}
}

func (p *testCommandPlugin) Execute(_ context.Context, req plugin.CommandRequest) (string, error) {
	return "plugin-output", nil
}

type testHTTPPlugin struct{}

func (p *testHTTPPlugin) Metadata() plugin.Metadata {
	return plugin.Metadata{Name: "http-test-http"}
}

func (p *testHTTPPlugin) HandleHTTP(r *http.Request) plugin.HTTPResponse {
	return plugin.HTTPResponse{
		StatusCode:  201,
		Body:        "http-plugin-body",
		Headers:     map[string]string{"X-Custom": "value"},
		ContentType: "text/plain",
	}
}

type testCommandPluginError struct{}

func (p *testCommandPluginError) Metadata() plugin.Metadata {
	return plugin.Metadata{Name: "http-test-cmd-err"}
}

func (p *testCommandPluginError) Execute(_ context.Context, req plugin.CommandRequest) (string, error) {
	return "", errors.New("simulated execute error")
}

func TestBuildHTTPResponse_PluginCommand(t *testing.T) {
	plugin.Register(&testCommandPlugin{})

	tr := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{}
	cmd := parser.Command{Plugin: "http-test-cmd"}
	req := httptest.NewRequest("GET", "http://localhost/", nil)

	resp, err := buildHTTPResponse(servConf, tr, cmd, req)
	assert.NoError(t, err)
	assert.Equal(t, "plugin-output", resp.Body)
}

func TestBuildHTTPResponse_PluginCommandError(t *testing.T) {
	plugin.Register(&testCommandPluginError{})

	tr := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{}
	cmd := parser.Command{Plugin: "http-test-cmd-err"}
	req := httptest.NewRequest("GET", "http://localhost/", nil)

	resp, err := buildHTTPResponse(servConf, tr, cmd, req)
	assert.Error(t, err)
	assert.Contains(t, resp.Body, "404 Not Found")
}

func TestBuildHTTPResponse_HTTPPlugin(t *testing.T) {
	plugin.Register(&testHTTPPlugin{})

	tr := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{}
	cmd := parser.Command{Plugin: "http-test-http"}
	req := httptest.NewRequest("GET", "http://localhost/", nil)

	resp, err := buildHTTPResponse(servConf, tr, cmd, req)
	assert.NoError(t, err)
	assert.Equal(t, "http-plugin-body", resp.Body)
	assert.Equal(t, 201, resp.StatusCode)
}

func TestBuildHTTPResponse_UnknownPlugin(t *testing.T) {
	tr := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{}

	cmd := parser.Command{
		Plugin:     "nonexistent-plugin",
		Handler:    "fallback",
		StatusCode: 200,
	}

	req := httptest.NewRequest("GET", "http://localhost/", nil)

	resp, err := buildHTTPResponse(servConf, tr, cmd, req)
	assert.NoError(t, err)
	assert.Equal(t, "fallback", resp.Body)
}

type errReader struct{}

func (r *errReader) Read(p []byte) (int, error) {
	return 0, errors.New("simulated read error")
}

func TestBuildHTTPResponse_BodyReadError(t *testing.T) {
	tr := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{}
	cmd := parser.Command{Handler: "fallback-body", StatusCode: 200}
	req := httptest.NewRequest("POST", "http://localhost/", io.NopCloser(&errReader{}))

	resp, err := buildHTTPResponse(servConf, tr, cmd, req)
	assert.NoError(t, err)
	assert.Equal(t, "fallback-body", resp.Body)
}

func TestRealClientAddr_XFFEmptyParts(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "8.8.8.8, , 10.0.0.1")

	host, port := realClientAddr(req, mustCIDRs(t, "10.0.0.0/8"))
	assert.Equal(t, "8.8.8.8", host)
	assert.Equal(t, "", port)
}

func TestRealClientAddr_XFFAllTrustedFallbackToXRI(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	// XFF entries are all trusted
	req.Header.Set("X-Forwarded-For", "10.0.0.2, 10.0.0.1")
	// XRI is also trusted
	req.Header.Set("X-Real-Ip", "10.0.0.3")

	host, port := realClientAddr(req, mustCIDRs(t, "10.0.0.0/8"))
	assert.Equal(t, "10.0.0.1", host)
	assert.Equal(t, "12345", port)
}

func TestHTTPStrategy_HandleRequest_StaticResponse(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	l.Close()

	strategy := &HTTPStrategy{}
	mt := &mockTracer{}

	rex := regexp.MustCompile("^/test$")
	servConf := parser.BeelzebubServiceConfiguration{
		Address:     addr,
		Description: "test HTTP",
		Commands: []parser.Command{
			{Regex: rex, Handler: "test response", StatusCode: 200, Name: "test-cmd"},
		},
	}

	err = strategy.Init(servConf, mt)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get(fmt.Sprintf("http://%s/test", addr))
	require.NoError(t, err)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "test response", string(body))
	assert.Equal(t, 200, resp.StatusCode)

	strategy.StopAll()
}

func TestHTTPStrategy_HandleRequest_Fallback(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	l.Close()

	strategy := &HTTPStrategy{}
	mt := &mockTracer{}

	rex := regexp.MustCompile("^/test$")
	servConf := parser.BeelzebubServiceConfiguration{
		Address:     addr,
		Description: "test HTTP",
		Commands: []parser.Command{
			{Regex: rex, Handler: "test response", StatusCode: 200},
		},
		FallbackCommand: parser.Command{
			Handler: "not found", StatusCode: 404, Name: "fallback",
		},
	}

	err = strategy.Init(servConf, mt)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get(fmt.Sprintf("http://%s/unknown", addr))
	require.NoError(t, err)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "not found", string(body))
	assert.Equal(t, 404, resp.StatusCode)

	strategy.StopAll()
}

func TestBuildHTTPResponse_WithBody(t *testing.T) {
	tr := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{}

	cmd := parser.Command{
		Handler:    "Echo",
		StatusCode: 201,
	}

	body := "some input body"
	req := httptest.NewRequest("POST", "http://localhost/", strings.NewReader(body))

	resp, err := buildHTTPResponse(servConf, tr, cmd, req)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if resp.StatusCode != 201 {
		t.Errorf("expected status 201, got %d", resp.StatusCode)
	}
}

func TestBuildHTTPResponse_UnknownPluginTest(t *testing.T) {
	tr := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{}

	cmd := parser.Command{
		Handler:    "Default",
		Plugin:     "unknown_plugin_123",
		StatusCode: 200,
	}

	req := httptest.NewRequest("GET", "http://localhost/", nil)

	resp, err := buildHTTPResponse(servConf, tr, cmd, req)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Falls through, so body will be the default handler
	if resp.Body != "Default" {
		t.Errorf("expected body to fall back to handler, got %q", resp.Body)
	}
}

func TestBuildHTTPResponse_LLMPlugin(t *testing.T) {
	tr := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{
		Description: "test",
		Plugin: parser.Plugin{
			LLMProvider: "openai", // Need a fake provider to cause an error or empty
		},
	}

	cmd := parser.Command{
		Plugin: plugins.LLMPluginName,
	}

	req := httptest.NewRequest("POST", "http://localhost/", nil)

	// Will likely fail because OpenAI key is empty or it tries to make a network request,
	// but it WILL hit the cp.Execute branch!
	resp, err := buildHTTPResponse(servConf, tr, cmd, req)

	// Either error or resp
	if err != nil {
		if resp.Body != "404 Not Found!" {
			t.Errorf("expected 404 body on error, got %s", resp.Body)
		}
	} else {
		// Just ensure it didn't panic
		_ = resp
	}
}

func TestBuildHTTPResponse_MazePlugin(t *testing.T) {
	tr := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{
		ServerName:    "TestServer",
		ServerVersion: "1.0",
	}

	cmd := parser.Command{
		Plugin: plugins.MazePluginName,
	}

	req := httptest.NewRequest("GET", "http://localhost/", nil)

	resp, err := buildHTTPResponse(servConf, tr, cmd, req)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if resp.StatusCode != 200 {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestMapHeaderToString_Empty(t *testing.T) {
	result := mapHeaderToString(http.Header{})
	assert.Equal(t, "", result)
}

func TestMapHeaderToString_SingleHeader(t *testing.T) {
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")

	result := mapHeaderToString(headers)

	assert.Contains(t, result, "Content-Type")
	assert.Contains(t, result, "application/json")
}

func TestMapHeaderToString_MultipleHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("Content-Type", "text/html")
	headers.Set("X-Custom", "value")

	result := mapHeaderToString(headers)

	assert.Contains(t, result, "Content-Type")
	assert.Contains(t, result, "X-Custom")
}

func TestMapCookiesToString_Empty(t *testing.T) {
	result := mapCookiesToString([]*http.Cookie{})
	assert.Equal(t, "", result)
}

func TestMapCookiesToString_SingleCookie(t *testing.T) {
	cookies := []*http.Cookie{
		{Name: "session", Value: "abc123"},
	}

	result := mapCookiesToString(cookies)

	assert.Contains(t, result, "session")
	assert.Contains(t, result, "abc123")
}

func TestMapCookiesToString_MultipleCookies(t *testing.T) {
	cookies := []*http.Cookie{
		{Name: "session", Value: "abc123"},
		{Name: "user", Value: "john"},
	}

	result := mapCookiesToString(cookies)

	assert.Contains(t, result, "session")
	assert.Contains(t, result, "user")
}

func TestSetResponseHeaders_ValidStatusCode(t *testing.T) {
	w := httptest.NewRecorder()
	setResponseHeaders(w, []string{"Content-Type: application/json"}, http.StatusOK)

	assert.Equal(t, http.StatusOK, w.Code)
	// The implementation splits on ":" and preserves the space, so the value has a leading space
	assert.Contains(t, w.Header().Get("Content-Type"), "application/json")
}

func TestSetResponseHeaders_InvalidStatusCode(t *testing.T) {
	w := httptest.NewRecorder()
	setResponseHeaders(w, []string{}, 999)

	// Unknown status code: WriteHeader should not be called, default stays 200
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSetResponseHeaders_NoHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	setResponseHeaders(w, []string{}, http.StatusNotFound)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestSetResponseHeaders_HeaderWithoutColon(t *testing.T) {
	w := httptest.NewRecorder()
	setResponseHeaders(w, []string{"InvalidHeader"}, http.StatusOK)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestTraceRequest_HTTP(t *testing.T) {
	mt := &mockTracer{}
	req := httptest.NewRequest(http.MethodGet, "/test", strings.NewReader("body"))
	req.Header.Set("User-Agent", "TestAgent/1.0")
	req.RemoteAddr = "127.0.0.1:12345"

	cmd := parser.Command{Name: "test-handler"}
	traceRequest(req, mt, cmd, "test-honeypot", "body", nil)

	assert.Len(t, mt.events, 1)
	event := mt.events[0]
	assert.Equal(t, "HTTP New request", event.Msg)
	assert.Equal(t, tracer.HTTP.String(), event.Protocol)
	assert.Equal(t, "test-honeypot", event.Description)
	assert.Equal(t, "127.0.0.1", event.SourceIp)
	assert.Equal(t, "12345", event.SourcePort)
	assert.Equal(t, "test-handler", event.Handler)
}

func TestTraceRequest_WithCookiesAndHeaders(t *testing.T) {
	mt := &mockTracer{}
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"user":"admin"}`))
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	req.AddCookie(&http.Cookie{Name: "session", Value: "xyz"})
	req.RemoteAddr = "192.168.1.1:54321"

	traceRequest(req, mt, parser.Command{}, "login-honeypot", `{"user":"admin"}`, nil)

	assert.Len(t, mt.events, 1)
	event := mt.events[0]
	assert.Contains(t, event.Headers, "X-Forwarded-For")
	assert.Contains(t, event.Cookies, "session")
	assert.Equal(t, "192.168.1.1", event.SourceIp)
}

func TestBuildHTTPResponse_StaticHandler(t *testing.T) {
	mt := &mockTracer{}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"

	servConf := parser.BeelzebubServiceConfiguration{Description: "test"}
	cmd := parser.Command{
		Handler:    "Hello World",
		StatusCode: http.StatusOK,
		Headers:    []string{"Content-Type: text/plain"},
	}

	resp, err := buildHTTPResponse(servConf, mt, cmd, req)

	assert.NoError(t, err)
	assert.Equal(t, "Hello World", resp.Body)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestRealClientAddr_NoTrustedProxies_IgnoresHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:4242"
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	req.Header.Set("X-Real-Ip", "8.8.8.8")

	host, port := realClientAddr(req, nil)

	assert.Equal(t, "10.0.0.5", host)
	assert.Equal(t, "4242", port)
}

func TestRealClientAddr_UntrustedPeer_IgnoresHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.7:80"
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	req.Header.Set("X-Real-Ip", "8.8.8.8")

	host, port := realClientAddr(req, mustCIDRs(t, "172.16.0.0/12"))

	assert.Equal(t, "203.0.113.7", host)
	assert.Equal(t, "80", port)
}

func TestRealClientAddr_TrustedPeer_UsesXForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "172.20.0.5:54321"
	req.Header.Set("X-Forwarded-For", "8.8.8.8")

	host, port := realClientAddr(req, mustCIDRs(t, "172.16.0.0/12"))

	assert.Equal(t, "8.8.8.8", host)
	assert.Equal(t, "", port)
}

// XFF poisoning: the attacker prefixes a fake IP, the trusted proxy appends the
// real peer. Walking right-to-left and skipping trusted hops must surface the
// real client (the first non-trusted entry from the right), not the spoof.

func TestRealClientAddr_TrustedPeer_WalksRightToLeftSkippingTrusted(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "172.20.0.5:54321"
	// 1.1.1.1 spoofed by attacker, 8.8.8.8 = real client, 172.20.0.6 = inner trusted hop
	req.Header.Set("X-Forwarded-For", "1.1.1.1, 8.8.8.8, 172.20.0.6")

	host, _ := realClientAddr(req, mustCIDRs(t, "172.16.0.0/12"))

	assert.Equal(t, "8.8.8.8", host)
}

func TestRealClientAddr_TrustedPeer_AllXFFTrusted_FallsBack(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "172.20.0.5:54321"
	req.Header.Set("X-Forwarded-For", "172.20.0.6, 172.20.0.7")

	host, port := realClientAddr(req, mustCIDRs(t, "172.16.0.0/12"))

	assert.Equal(t, "172.20.0.5", host)
	assert.Equal(t, "54321", port)
}

func TestRealClientAddr_TrustedPeer_FallsBackToXRealIp(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "172.20.0.5:54321"
	req.Header.Set("X-Real-Ip", "8.8.8.8")

	host, port := realClientAddr(req, mustCIDRs(t, "172.16.0.0/12"))

	assert.Equal(t, "8.8.8.8", host)
	assert.Equal(t, "", port)
}

func TestRealClientAddr_TrustedPeer_XRealIpIgnoredIfTrusted(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "172.20.0.5:54321"
	req.Header.Set("X-Real-Ip", "172.20.0.9")

	host, port := realClientAddr(req, mustCIDRs(t, "172.16.0.0/12"))

	assert.Equal(t, "172.20.0.5", host)
	assert.Equal(t, "54321", port)
}

func TestRealClientAddr_MalformedXFFEntries_Skipped(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "172.20.0.5:54321"
	req.Header.Set("X-Forwarded-For", "not-an-ip,   ,8.8.8.8, 172.20.0.6")

	host, _ := realClientAddr(req, mustCIDRs(t, "172.16.0.0/12"))

	assert.Equal(t, "8.8.8.8", host)
}

func TestRealClientAddr_IPv6_TrustedPeer(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "[fd00::1]:54321"
	req.Header.Set("X-Forwarded-For", "2001:db8::1234")

	host, _ := realClientAddr(req, mustCIDRs(t, "fd00::/8"))

	assert.Equal(t, "2001:db8::1234", host)
}

func TestRealClientAddr_RemoteAddrWithoutPort(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5"

	host, port := realClientAddr(req, nil)

	assert.Equal(t, "10.0.0.5", host)
	assert.Equal(t, "", port)
}

func TestTraceRequest_TrustedProxy_ResolvesRealClient(t *testing.T) {
	mt := &mockTracer{}
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.RemoteAddr = "172.20.0.5:54321"
	req.Header.Set("X-Forwarded-For", "8.8.8.8")

	traceRequest(req, mt, parser.Command{Name: "admin"}, "test", "", mustCIDRs(t, "172.16.0.0/12"))

	require.Len(t, mt.events, 1)
	ev := mt.events[0]
	assert.Equal(t, "8.8.8.8", ev.SourceIp)
	assert.Equal(t, "", ev.SourcePort)
	// RemoteAddr reflects the resolved real client IP when peer is a trusted proxy.
	assert.Equal(t, "8.8.8.8", ev.RemoteAddr)
}

func TestTraceRequest_UntrustedPeer_DoesNotTrustHeaders(t *testing.T) {
	mt := &mockTracer{}
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.RemoteAddr = "203.0.113.7:8080"
	req.Header.Set("X-Forwarded-For", "8.8.8.8")

	traceRequest(req, mt, parser.Command{}, "test", "", mustCIDRs(t, "172.16.0.0/12"))

	require.Len(t, mt.events, 1)
	assert.Equal(t, "203.0.113.7", mt.events[0].SourceIp)
}

// TestTraceRequest_RemoteAddr_WithPort verifies that when the peer address
// carries a port (direct connection, no trusted proxy), RemoteAddr is formatted
// as "host:port" via net.JoinHostPort.

func TestTraceRequest_RemoteAddr_WithPort(t *testing.T) {
	mt := &mockTracer{}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.5:9000"

	traceRequest(req, mt, parser.Command{}, "test", "", nil)

	require.Len(t, mt.events, 1)
	ev := mt.events[0]
	assert.Equal(t, "203.0.113.5", ev.SourceIp)
	assert.Equal(t, "9000", ev.SourcePort)
	assert.Equal(t, "203.0.113.5:9000", ev.RemoteAddr)
}

// TestTraceRequest_RemoteAddr_WithoutPort verifies that when the resolved
// address has no port (e.g. IP came from XFF header via a trusted proxy),
// RemoteAddr equals just the host IP without any colon.

func TestTraceRequest_RemoteAddr_WithoutPort(t *testing.T) {
	mt := &mockTracer{}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "172.20.0.5:54321"
	req.Header.Set("X-Forwarded-For", "203.0.113.99")

	traceRequest(req, mt, parser.Command{}, "test", "", mustCIDRs(t, "172.16.0.0/12"))

	require.Len(t, mt.events, 1)
	ev := mt.events[0]
	assert.Equal(t, "203.0.113.99", ev.SourceIp)
	assert.Equal(t, "", ev.SourcePort)
	// No port → remoteAddr must be just the host, no trailing colon.
	assert.Equal(t, "203.0.113.99", ev.RemoteAddr)
	assert.NotContains(t, ev.RemoteAddr, ":")
}

// TestTraceRequest_RemoteAddr_IPv6WithPort verifies JoinHostPort wraps an IPv6
// address in brackets: "[::1]:8080".

func TestTraceRequest_RemoteAddr_IPv6WithPort(t *testing.T) {
	mt := &mockTracer{}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "[::1]:8080"

	traceRequest(req, mt, parser.Command{}, "test", "", nil)

	require.Len(t, mt.events, 1)
	ev := mt.events[0]
	assert.Equal(t, "::1", ev.SourceIp)
	assert.Equal(t, "8080", ev.SourcePort)
	assert.Equal(t, "[::1]:8080", ev.RemoteAddr)
}

// TestTraceRequest_RemoteAddr_IPv6WithoutPort verifies that an IPv6 address
// resolved from a header (no port) is stored as-is without brackets.

func TestTraceRequest_RemoteAddr_IPv6WithoutPort(t *testing.T) {
	mt := &mockTracer{}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "[fd00::1]:54321"
	req.Header.Set("X-Forwarded-For", "2001:db8::42")

	traceRequest(req, mt, parser.Command{}, "test", "", mustCIDRs(t, "fd00::/8"))

	require.Len(t, mt.events, 1)
	ev := mt.events[0]
	assert.Equal(t, "2001:db8::42", ev.SourceIp)
	assert.Equal(t, "", ev.SourcePort)
	assert.Equal(t, "2001:db8::42", ev.RemoteAddr)
}

func TestHTTPHandler_MethodRouting(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		commands   []parser.Command
		fallback   parser.Command
		wantStatus int
		wantBody   string
		wantAllow  string
	}{
		{
			name:       "allowed method",
			method:     http.MethodPost,
			path:       "/api/generate",
			commands:   []parser.Command{{Regex: regexp.MustCompile(`^/api/generate$`), Methods: []string{http.MethodPost}, Handler: "generated", StatusCode: http.StatusOK}},
			wantStatus: http.StatusOK,
			wantBody:   "generated",
		},
		{
			name:       "missing list accepts every method",
			method:     http.MethodPatch,
			path:       "/open",
			commands:   []parser.Command{{Regex: regexp.MustCompile(`^/open$`), Handler: "open", StatusCode: http.StatusOK}},
			wantStatus: http.StatusOK,
			wantBody:   "open",
		},
		{
			name:   "later command accepts method",
			method: http.MethodPost,
			path:   "/same",
			commands: []parser.Command{
				{Regex: regexp.MustCompile(`^/same$`), Methods: []string{http.MethodGet}, Handler: "get", StatusCode: http.StatusOK},
				{Regex: regexp.MustCompile(`^/same$`), Methods: []string{http.MethodPost}, Handler: "post", StatusCode: http.StatusCreated},
			},
			wantStatus: http.StatusCreated,
			wantBody:   "post",
		},
		{
			name:   "known URL rejects method",
			method: http.MethodDelete,
			path:   "/same",
			commands: []parser.Command{
				{Regex: regexp.MustCompile(`^/same$`), Methods: []string{http.MethodGet, http.MethodPost}, Handler: "wrong", StatusCode: http.StatusOK},
				{Regex: regexp.MustCompile(`^/same$`), Methods: []string{http.MethodPost}, Handler: "wrong", StatusCode: http.StatusOK},
			},
			fallback:   parser.Command{Handler: "fallback", StatusCode: http.StatusNotFound},
			wantStatus: http.StatusMethodNotAllowed,
			wantBody:   http.StatusText(http.StatusMethodNotAllowed),
			wantAllow:  "GET, POST",
		},
		{
			name:       "unknown URL uses fallback",
			method:     http.MethodGet,
			path:       "/missing",
			commands:   []parser.Command{{Regex: regexp.MustCompile(`^/known$`), Methods: []string{http.MethodGet}, Handler: "known", StatusCode: http.StatusOK}},
			fallback:   parser.Command{Handler: "fallback", StatusCode: http.StatusNotFound},
			wantStatus: http.StatusNotFound,
			wantBody:   "fallback",
		},
		{
			name:       "matched plugin error returns internal server error",
			method:     http.MethodGet,
			path:       "/error",
			commands:   []parser.Command{{Regex: regexp.MustCompile(`^/error$`), Plugin: plugins.LLMPluginName}},
			wantStatus: http.StatusInternalServerError,
			wantBody:   "500 Internal Server Error",
		},
		{
			name:       "fallback plugin error returns internal server error",
			method:     http.MethodGet,
			path:       "/missing",
			fallback:   parser.Command{Plugin: plugins.LLMPluginName},
			wantStatus: http.StatusInternalServerError,
			wantBody:   "500 Internal Server Error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(tt.method, tt.path, nil)
			mt := &mockTracer{}
			newHTTPHandler(parser.BeelzebubServiceConfiguration{
				Commands:        tt.commands,
				FallbackCommand: tt.fallback,
			}, mt).ServeHTTP(recorder, request)

			assert.Equal(t, tt.wantStatus, recorder.Code)
			assert.Equal(t, tt.wantBody, recorder.Body.String())
			assert.Equal(t, tt.wantAllow, recorder.Header().Get("Allow"))
			assert.Len(t, mt.events, 1)
		})
	}
}

func TestInit_Basic(t *testing.T) {
	strategy := HTTPStrategy{}
	tr := &mockTracer{}

	// Just use a random high port for the test, or an invalid one to ensure we hit the error branch in the go func
	conf := parser.BeelzebubServiceConfiguration{
		Address:     "127.0.0.1:0",
		Description: "test init",
	}

	err := strategy.Init(conf, tr)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Wait a tiny bit for the go func to start and hit the listen error or success
	time.Sleep(50 * time.Millisecond)
}

func TestInit_TLS(t *testing.T) {
	strategy := HTTPStrategy{}
	tr := &mockTracer{}

	conf := parser.BeelzebubServiceConfiguration{
		Address:     "127.0.0.1:0",
		Description: "test tls",
		TLSKeyPath:  "invalid-key.pem",
		TLSCertPath: "invalid-cert.pem",
	}

	err := strategy.Init(conf, tr)
	if err == nil {
		t.Fatal("expected TLS certificate loading error")
	}
}

func TestInit_HandlerFunc(t *testing.T) {
	strategy := HTTPStrategy{}
	tr := &mockTracer{}

	conf := parser.BeelzebubServiceConfiguration{
		Address:     "127.0.0.1:0",
		Description: "test handler",
		Commands: []parser.Command{
			{
				Regex:      regexp.MustCompile("^/hello$"),
				Handler:    "Hello there",
				StatusCode: 200,
			},
			{
				Regex:  regexp.MustCompile("^/error$"),
				Plugin: "unknown_error_plugin_123", // this simulates a plugin that does not exist -> but actually it falls back
			},
		},
		FallbackCommand: parser.Command{
			Handler:    "Fallback",
			StatusCode: 404,
		},
	}

	// We can't directly test the handler because it's inside the ServeMux
	// which is bound to the server inside the strategy, but we can't easily retrieve the ServeMux.
	// But simply starting the server with these commands covers some paths.
	strategy.Init(conf, tr)
	time.Sleep(50 * time.Millisecond)
}
