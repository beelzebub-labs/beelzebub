package MCP

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/protocols"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockTracer struct {
	events []tracer.Event
}

func (m *mockTracer) TraceEvent(event tracer.Event) {
	m.events = append(m.events, event)
}

func TestMCPStrategy_Init_NoTools(t *testing.T) {
	strategy := &MCPStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:     "127.0.0.1:0",
		Description: "test MCP server",
		Protocol:    "mcp",
		Tools:       []parser.Tool{},
	}

	err := strategy.Init(servConf, mt)
	assert.NoError(t, err)
}

func TestMCPStrategy_Init_ToolWithNoParams(t *testing.T) {
	strategy := &MCPStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:     "127.0.0.1:0",
		Description: "test MCP server",
		Protocol:    "mcp",
		Tools: []parser.Tool{
			{
				Name:        "tool:no-params",
				Description: "A tool with no params",
				Params:      nil,
				Handler:     "response",
			},
		},
	}

	// Tool with no params should be skipped (logged as error) without panicking
	err := strategy.Init(servConf, mt)
	assert.NoError(t, err)
}

func TestMCPStrategy_Init_ToolWithParams(t *testing.T) {
	strategy := &MCPStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:     "127.0.0.1:0",
		Description: "test MCP server",
		Protocol:    "mcp",
		Tools: []parser.Tool{
			{
				Name:        "greet",
				Description: "A greeting tool",
				Handler:     "Hello!",
				Params: []parser.Param{
					{
						Name:        "name",
						Description: "Name to greet",
					},
				},
			},
		},
	}

	err := strategy.Init(servConf, mt)
	assert.NoError(t, err)
}

func TestMCPStrategy_Init_BindError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	strategy := &MCPStrategy{}
	err = strategy.Init(parser.BeelzebubServiceConfiguration{Address: listener.Addr().String()}, &mockTracer{})
	assert.Error(t, err)
	assert.Empty(t, strategy.servers)
}

func TestMCPStrategy_Init_ToolWithAnnotations(t *testing.T) {
	strategy := &MCPStrategy{}
	mt := &mockTracer{}
	readOnly := true
	destructive := false
	idempotent := true
	openWorld := false

	servConf := parser.BeelzebubServiceConfiguration{
		Address:     "127.0.0.1:0",
		Description: "test MCP server with annotations",
		Protocol:    "mcp",
		Tools: []parser.Tool{
			{
				Name:        "annotated-tool",
				Description: "An annotated tool",
				Handler:     "annotated response",
				Annotations: &parser.ToolAnnotations{
					Title:           "My Tool",
					ReadOnlyHint:    &readOnly,
					DestructiveHint: &destructive,
					IdempotentHint:  &idempotent,
					OpenWorldHint:   &openWorld,
				},
				Params: []parser.Param{
					{
						Name:        "input",
						Description: "Input param",
					},
				},
			},
		},
	}

	err := strategy.Init(servConf, mt)
	assert.NoError(t, err)
}

func TestMCPStrategy_Stop_Success(t *testing.T) {
	strategy := &MCPStrategy{}
	config := parser.BeelzebubServiceConfiguration{Address: "127.0.0.1:0", Protocol: "mcp"}
	require.NoError(t, strategy.Init(config, &mockTracer{}))
	require.NoError(t, strategy.Stop(config))
	assert.Empty(t, strategy.servers)
}

func TestMCPStrategy_handleDeploy_InvalidRegexAndTrustedProxies(t *testing.T) {
	strategy := &MCPStrategy{}
	tr := &mockTracer{}
	request := func(config string) string {
		req := mcp.CallToolRequest{}
		req.Params.Arguments = map[string]interface{}{"config_yaml": config}
		result, err := strategy.handleDeploy(context.Background(), req, parser.BeelzebubServiceConfiguration{}, tr, "", "")
		require.NoError(t, err)
		return result.Content[0].(mcp.TextContent).Text
	}

	assert.Contains(t, request("protocol: ssh\ncommands:\n  - regex: '[invalid'\n"), "invalid regex")
	assert.Contains(t, request("protocol: ssh\ntrustedProxies: ['[invalid']\n"), "invalid trustedProxies")
}

func TestMCPStrategy_StopAll(t *testing.T) {
	strategy := &MCPStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:     "127.0.0.1:0",
		Description: "test MCP server",
		Protocol:    "mcp",
		Tools:       []parser.Tool{},
	}

	assert.NoError(t, strategy.Init(servConf, mt))
	assert.Len(t, strategy.servers, 1)

	assert.NoError(t, strategy.StopAll())
	assert.Nil(t, strategy.servers)
}

func TestMCPStrategy_StopAll_Empty(t *testing.T) {
	strategy := &MCPStrategy{}
	assert.NoError(t, strategy.StopAll())
}

func TestMCPStrategy_StopAll_MultipleInits(t *testing.T) {
	strategy := &MCPStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:     "127.0.0.1:0",
		Description: "test MCP server",
		Protocol:    "mcp",
		Tools:       []parser.Tool{},
	}

	assert.NoError(t, strategy.Init(servConf, mt))
	assert.NoError(t, strategy.Init(servConf, mt))
	assert.Equal(t, 1, len(strategy.servers))

	assert.NoError(t, strategy.StopAll())
	assert.Nil(t, strategy.servers)
}

type failOnCloseListener struct {
	net.Listener
	closeErr error
}

func (f *failOnCloseListener) Close() error {
	f.Listener.Close()
	return f.closeErr
}

func TestMCPStrategy_StopAll_ShutdownError(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	failL := &failOnCloseListener{Listener: l, closeErr: errors.New("simulated close error")}

	rawSrv := &http.Server{Handler: http.NewServeMux()}
	go rawSrv.Serve(failL)
	time.Sleep(50 * time.Millisecond)

	mcpServer := server.NewMCPServer("test", "1.0.0")
	httpServer := server.NewStreamableHTTPServer(mcpServer,
		server.WithStreamableHTTPServer(rawSrv),
	)

	strategy := &MCPStrategy{}
	strategy.servers = map[string]*server.StreamableHTTPServer{"test:0": httpServer}

	err = strategy.StopAll()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "simulated close error")
	assert.Nil(t, strategy.servers)
}

func TestMCPStrategy_Stop_ShutdownError(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	failL := &failOnCloseListener{Listener: l, closeErr: errors.New("simulated close error")}

	rawSrv := &http.Server{Handler: http.NewServeMux()}
	go rawSrv.Serve(failL)
	time.Sleep(50 * time.Millisecond)

	mcpServer := server.NewMCPServer("test", "1.0.0")
	httpServer := server.NewStreamableHTTPServer(mcpServer,
		server.WithStreamableHTTPServer(rawSrv),
	)

	strategy := &MCPStrategy{}
	strategy.servers = map[string]*server.StreamableHTTPServer{"test:0": httpServer}

	err = strategy.Stop(parser.BeelzebubServiceConfiguration{Address: "test:0"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "simulated close error")
	// On error, Stop returns immediately without deleting from map
	assert.Len(t, strategy.servers, 1)
}

func TestMCPStrategy_Stop_ServerNotFound(t *testing.T) {
	strategy := &MCPStrategy{}
	strategy.servers = map[string]*server.StreamableHTTPServer{"other:0": nil}

	err := strategy.Stop(parser.BeelzebubServiceConfiguration{Address: "test:0"})
	assert.NoError(t, err)
}

func TestMCPStrategy_handleDeploy_MissingConfig(t *testing.T) {
	strategy := &MCPStrategy{}
	mt := &mockTracer{}

	req := mcp.CallToolRequest{}
	req.Params.Name = deployDeployToolName
	req.Params.Arguments = map[string]interface{}{}
	ctx := context.WithValue(context.Background(), remoteAddrCtxKey{}, "10.0.0.1:12345")
	result, err := strategy.handleDeploy(ctx, req, parser.BeelzebubServiceConfiguration{}, mt, "10.0.0.1", "12345")
	assert.NoError(t, err)
	assert.Contains(t, result.Content[0].(mcp.TextContent).Text, "missing required parameter")
}

func TestMCPStrategy_handleDeploy_InvalidYAML(t *testing.T) {
	strategy := &MCPStrategy{}
	mt := &mockTracer{}

	req := mcp.CallToolRequest{}
	req.Params.Name = deployDeployToolName
	req.Params.Arguments = map[string]interface{}{"config_yaml": "{{ invalid yaml }"}
	ctx := context.WithValue(context.Background(), remoteAddrCtxKey{}, "10.0.0.1:12345")

	result, err := strategy.handleDeploy(ctx, req, parser.BeelzebubServiceConfiguration{}, mt, "10.0.0.1", "12345")
	assert.NoError(t, err)
	assert.Contains(t, result.Content[0].(mcp.TextContent).Text, "invalid YAML")
}

func TestMCPStrategy_handleDeploy_NoProtocol(t *testing.T) {
	strategy := &MCPStrategy{}
	mt := &mockTracer{}

	req := mcp.CallToolRequest{}
	req.Params.Name = deployDeployToolName
	req.Params.Arguments = map[string]interface{}{"config_yaml": "address: \":9999\""}
	ctx := context.WithValue(context.Background(), remoteAddrCtxKey{}, "10.0.0.1:12345")

	result, err := strategy.handleDeploy(ctx, req, parser.BeelzebubServiceConfiguration{}, mt, "10.0.0.1", "12345")
	assert.NoError(t, err)
	assert.Contains(t, result.Content[0].(mcp.TextContent).Text, "'protocol' field")
}

func TestMCPStrategy_handleDeploy_DeployFnError(t *testing.T) {
	deployErr := errors.New("deployment rejected")
	strategy := &MCPStrategy{
		deployFn: func(cfg parser.BeelzebubServiceConfiguration) error {
			return deployErr
		},
	}
	mt := &mockTracer{}

	req := mcp.CallToolRequest{}
	req.Params.Name = deployDeployToolName
	req.Params.Arguments = map[string]interface{}{"config_yaml": "protocol: ssh\naddress: \":2222\""}

	ctx := context.WithValue(context.Background(), remoteAddrCtxKey{}, "10.0.0.1:12345")
	result, err := strategy.handleDeploy(ctx, req, parser.BeelzebubServiceConfiguration{}, mt, "10.0.0.1", "12345")
	assert.NoError(t, err)
	assert.Contains(t, result.Content[0].(mcp.TextContent).Text, "deployment rejected")
}

func TestMCPStrategy_handleDeploy_Success(t *testing.T) {
	var deployed parser.BeelzebubServiceConfiguration
	strategy := &MCPStrategy{
		deployFn: func(cfg parser.BeelzebubServiceConfiguration) error {
			deployed = cfg
			return nil
		},
	}
	mt := &mockTracer{}

	req := mcp.CallToolRequest{}
	req.Params.Name = deployDeployToolName
	req.Params.Arguments = map[string]interface{}{
		"config_yaml": "protocol: ssh\naddress: \":2222\"\ndescription: Deployed SSH\npasswordRegex: .*\n",
	}

	ctx := context.WithValue(context.Background(), remoteAddrCtxKey{}, "10.0.0.1:12345")
	result, err := strategy.handleDeploy(ctx, req, parser.BeelzebubServiceConfiguration{}, mt, "10.0.0.1", "12345")
	assert.NoError(t, err)
	assert.Contains(t, result.Content[0].(mcp.TextContent).Text, "success")
	assert.Equal(t, "ssh", deployed.Protocol)
	assert.Equal(t, ":2222", deployed.Address)

	// Verify trace event was emitted
	assert.Len(t, mt.events, 1)
	assert.Contains(t, mt.events[0].Msg, "Deployed honeypot via MCP")
	assert.Equal(t, tracer.Stateless.String(), mt.events[0].Status)
}

func TestMCPStrategy_handleDeploy_InvalidArgumentsType(t *testing.T) {
	strategy := &MCPStrategy{}
	mt := &mockTracer{}

	req := mcp.CallToolRequest{}
	req.Params.Name = deployDeployToolName
	req.Params.Arguments = "not-a-map"

	ctx := context.WithValue(context.Background(), remoteAddrCtxKey{}, "10.0.0.1:12345")
	result, err := strategy.handleDeploy(ctx, req, parser.BeelzebubServiceConfiguration{}, mt, "10.0.0.1", "12345")
	assert.NoError(t, err)
	assert.Contains(t, result.Content[0].(mcp.TextContent).Text, "invalid arguments format")
}

func TestMCPStrategy_handleDeploy_ConfigYAMLNotString(t *testing.T) {
	strategy := &MCPStrategy{}
	mt := &mockTracer{}

	req := mcp.CallToolRequest{}
	req.Params.Name = deployDeployToolName
	req.Params.Arguments = map[string]interface{}{"config_yaml": 12345}

	ctx := context.WithValue(context.Background(), remoteAddrCtxKey{}, "10.0.0.1:12345")
	result, err := strategy.handleDeploy(ctx, req, parser.BeelzebubServiceConfiguration{}, mt, "10.0.0.1", "12345")
	assert.NoError(t, err)
	assert.Contains(t, result.Content[0].(mcp.TextContent).Text, "config_yaml must be a string")
}

func TestMCPStrategy_SetDeployFn(t *testing.T) {
	strategy := &MCPStrategy{}
	assert.Nil(t, strategy.deployFn)

	fn := func(cfg parser.BeelzebubServiceConfiguration) error { return nil }
	strategy.SetDeployFn(fn)
	assert.NotNil(t, strategy.deployFn)
}

func TestMCPStrategy_Init_InvokesToolHandler(t *testing.T) {
	strategy := &MCPStrategy{}
	tr := &mockTracer{}
	config := parser.BeelzebubServiceConfiguration{
		Address:     "127.0.0.1:0",
		Description: "callback test",
		Protocol:    "mcp",
		Tools: []parser.Tool{{
			Name:        "echo",
			Description: "echoes a response",
			Handler:     "echo response",
			Params:      []parser.Param{{Name: "value", Description: "value"}},
		}},
	}
	require.NoError(t, strategy.Init(config, tr))
	defer strategy.StopAll()

	ts := httptest.NewServer(strategy.servers[config.Address])
	defer ts.Close()
	post := func(body string, session string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewBufferString(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		resp, err := ts.Client().Do(req)
		require.NoError(t, err)
		return resp
	}

	initResp := post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+mcp.LATEST_PROTOCOL_VERSION+`","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`, "")
	defer initResp.Body.Close()
	_, _ = io.Copy(io.Discard, initResp.Body)
	require.NotEmpty(t, initResp.Header.Get("Mcp-Session-Id"))

	callResp := post(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"value":"hello"}}}`, initResp.Header.Get("Mcp-Session-Id"))
	defer callResp.Body.Close()
	body, err := io.ReadAll(callResp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, callResp.StatusCode)
	assert.Contains(t, string(body), "echo response")
	require.Len(t, tr.events, 1)
	assert.Equal(t, "New MCP tool invocation", tr.events[0].Msg)
}

func TestMCPStrategy_RegistryFactory(t *testing.T) {
	group := protocols.NewServiceGroupFromRegistry(func(tracer.Event) {})
	assert.NotNil(t, group.StrategyForProtocol("mcp"))
}
