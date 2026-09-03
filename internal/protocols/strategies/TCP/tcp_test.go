package TCP

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/historystore"
	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	"github.com/beelzebub-labs/beelzebub/v3/internal/protocols"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type tcpTestCommandPlugin struct{}

func (p *tcpTestCommandPlugin) Metadata() plugin.Metadata {
	return plugin.Metadata{Name: "tcp-test-cmd"}
}

func (p *tcpTestCommandPlugin) Execute(_ context.Context, req plugin.CommandRequest) (string, error) {
	return "tcp-plugin-output", nil
}

type tcpErrorCommandPlugin struct{}

func (p *tcpErrorCommandPlugin) Metadata() plugin.Metadata {
	return plugin.Metadata{Name: "tcp-error-cmd"}
}

func (p *tcpErrorCommandPlugin) Execute(context.Context, plugin.CommandRequest) (string, error) {
	return "", errors.New("tcp plugin failure")
}

type tcpFailingListener struct {
	net.Listener
	err error
}

type tcpScriptedListener struct {
	accepted net.Conn
	step     int
}

func (l *tcpScriptedListener) Accept() (net.Conn, error) {
	l.step++
	switch l.step {
	case 1:
		return l.accepted, nil
	case 2:
		return nil, errors.New("temporary accept failure")
	default:
		return nil, net.ErrClosed
	}
}

func (l *tcpScriptedListener) Close() error   { return nil }
func (l *tcpScriptedListener) Addr() net.Addr { return scriptedAddr("tcp-scripted") }

type scriptedAddr string

func (a scriptedAddr) Network() string { return "tcp" }
func (a scriptedAddr) String() string  { return string(a) }

func (l *tcpFailingListener) Close() error {
	_ = l.Listener.Close()
	return l.err
}

type mockTracer struct {
	events []tracer.Event
}

func (m *mockTracer) TraceEvent(event tracer.Event) {
	m.events = append(m.events, event)
}

func newStrategyWithSessions() *TCPStrategy {
	return &TCPStrategy{Sessions: historystore.NewHistoryStore()}
}

func TestHandleTCPConnection_NoCommands_Legacy(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		Commands:               []parser.Command{},
	}
	strategy := newStrategyWithSessions()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTCPConnection(server, servConf, mt, strategy)
	}()

	client.Write([]byte("hello world"))
	client.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for connection handler")
	}

	assert.GreaterOrEqual(t, len(mt.events), 1)
	assert.Equal(t, tracer.Stateless.String(), mt.events[0].Status)
}

func TestHandleTCPConnection_WithBanner(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		Banner:                 "Welcome to TCP honeypot",
	}
	strategy := newStrategyWithSessions()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTCPConnection(server, servConf, mt, strategy)
	}()

	buf := make([]byte, 1024)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("failed to read banner: %v", err)
	}

	banner := strings.TrimSpace(string(buf[:n]))
	assert.Equal(t, "Welcome to TCP honeypot", banner)

	client.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for connection handler")
	}
}

func TestHandleTCPConnection_WithCommands(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	rex, _ := regexp.Compile("ping")
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		Commands: []parser.Command{
			{
				Regex:   rex,
				Handler: "pong",
				Name:    "ping-command",
			},
		},
	}
	strategy := newStrategyWithSessions()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTCPConnection(server, servConf, mt, strategy)
	}()

	client.Write([]byte("ping\n"))
	time.Sleep(100 * time.Millisecond)

	buf := make([]byte, 1024)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	assert.Equal(t, "pong", strings.TrimSpace(string(buf[:n])))

	client.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for connection handler")
	}
}

func TestHandleTCPConnection_NonUTF8Bytes(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
	}
	strategy := newStrategyWithSessions()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTCPConnection(server, servConf, mt, strategy)
	}()

	client.Write([]byte{0xff, 0xfe, 0x00, 0x01})
	client.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for connection handler")
	}

	assert.GreaterOrEqual(t, len(mt.events), 1)
	if len(mt.events) > 0 {
		assert.NotEmpty(t, mt.events[0].CommandRaw)
	}
}

func TestTCPStrategy_Init_InvalidAddress(t *testing.T) {
	strategy := newStrategyWithSessions()
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:     "invalid-address-no-port",
		Description: "test",
	}

	err := strategy.Init(servConf, mt)
	assert.Error(t, err)
}

func TestTCPStrategy_StopAll(t *testing.T) {
	strategy := newStrategyWithSessions()
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:     "127.0.0.1:0",
		Description: "test",
	}

	assert.NoError(t, strategy.Init(servConf, mt))
	assert.Len(t, strategy.listeners, 1)

	assert.NoError(t, strategy.StopAll())
	assert.Nil(t, strategy.listeners)
}

func TestTCPStrategy_StopAll_Empty(t *testing.T) {
	strategy := newStrategyWithSessions()
	assert.NoError(t, strategy.StopAll())
}

func TestTCPStrategy_StopAll_AcceptsReturnsClosed(t *testing.T) {
	strategy := newStrategyWithSessions()
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:     "127.0.0.1:0",
		Description: "test",
	}

	assert.NoError(t, strategy.Init(servConf, mt))
	assert.Len(t, strategy.listeners, 1)

	// Stop all, which closes the listener
	assert.NoError(t, strategy.StopAll())

	// The accept loop should exit cleanly (net.ErrClosed)
	time.Sleep(50 * time.Millisecond)
}

func TestTCPStrategy_Stop_WaitsOnlyForRequestedListener(t *testing.T) {
	strategy := newStrategyWithSessions()
	defer strategy.StopAll()

	addresses := make([]string, 0, 2)
	for range 2 {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addresses = append(addresses, listener.Addr().String())
		require.NoError(t, listener.Close())
	}

	for _, address := range addresses {
		require.NoError(t, strategy.Init(parser.BeelzebubServiceConfiguration{
			Protocol: "tcp",
			Address:  address,
		}, &mockTracer{}))
	}

	done := make(chan error, 1)
	go func() {
		done <- strategy.Stop(parser.BeelzebubServiceConfiguration{Address: addresses[0]})
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("stopping one TCP listener waited for another listener")
	}

	assert.Len(t, strategy.listeners, 1)
}

func TestTCPStrategy_StopAll_AlreadyClosed(t *testing.T) {
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	listener.Close()

	strategy := newStrategyWithSessions()
	strategy.listeners = map[string]net.Listener{"test:0": listener}

	// Close on an already-closed listener returns an error now
	err := strategy.StopAll()
	assert.Error(t, err)
	assert.Nil(t, strategy.listeners)
}

func TestTCPStrategy_Stop_CloseErrorFromListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.New(t).NoError(err)
	addr := listener.Addr().String()
	listener.Close()

	// Re-listen so we have a fresh listener to close
	listener2, err := net.Listen("tcp", addr)
	require.New(t).NoError(err)

	strategy := newStrategyWithSessions()
	strategy.listeners = map[string]net.Listener{addr: listener2}

	servConf := parser.BeelzebubServiceConfiguration{Address: addr}
	err = strategy.Stop(servConf)
	assert.NoError(t, err)
	assert.Empty(t, strategy.listeners)
}

func TestTCPStrategy_Stop_ServerNotFound(t *testing.T) {
	strategy := newStrategyWithSessions()
	strategy.listeners = map[string]net.Listener{"other:0": nil}

	err := strategy.Stop(parser.BeelzebubServiceConfiguration{Address: "test:0"})
	assert.NoError(t, err)
}

func TestTCPStrategy_Stop_CloseError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	strategy := newStrategyWithSessions()
	strategy.listeners = map[string]net.Listener{"test:0": &tcpFailingListener{Listener: listener, err: errors.New("close failed")}}

	err = strategy.Stop(parser.BeelzebubServiceConfiguration{Address: "test:0"})
	assert.EqualError(t, err, "close failed")
}

func TestTCPStrategy_Init_OverwriteAndLazySessions(t *testing.T) {
	strategy := &TCPStrategy{}
	config := parser.BeelzebubServiceConfiguration{Address: "127.0.0.1:0", Protocol: "tcp"}
	require.NoError(t, strategy.Init(config, &mockTracer{}))
	require.NoError(t, strategy.Init(config, &mockTracer{}))
	require.NoError(t, strategy.StopAll())
}

func TestTCPStrategy_Init_AcceptErrorAndAcceptedConnection(t *testing.T) {
	client, server := net.Pipe()
	client.Close()
	oldListen := listenTCP
	listenTCP = func(string, string) (net.Listener, error) {
		return &tcpScriptedListener{accepted: server}, nil
	}
	t.Cleanup(func() { listenTCP = oldListen })

	strategy := &TCPStrategy{}
	require.NoError(t, strategy.Init(parser.BeelzebubServiceConfiguration{Address: "scripted"}, &mockTracer{}))
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, strategy.StopAll())
}

func TestHexEscapeNonPrintable(t *testing.T) {
	tests := []struct {
		input    []byte
		expected string
	}{
		{[]byte("hello"), "hello"},
		{[]byte{0x00, 0x01, 0x02}, `\x00\x01\x02`},
		{[]byte("test\x00end"), "test\\x00end"},
		{[]byte{0x7f}, `\x7f`},
		{[]byte{0x5c}, `\x5c`}, // backslash
	}
	for _, tt := range tests {
		result := hexEscapeNonPrintable(tt.input)
		assert.Equal(t, tt.expected, result)
	}
}

func TestHandleTCPConnection_WithPluginCommand(t *testing.T) {
	plugin.Register(&tcpTestCommandPlugin{})

	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	rex, _ := regexp.Compile("ping")
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		Commands: []parser.Command{
			{
				Regex:  rex,
				Plugin: "tcp-test-cmd",
				Name:   "ping-command",
			},
		},
	}
	strategy := newStrategyWithSessions()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTCPConnection(server, servConf, mt, strategy)
	}()

	client.Write([]byte("ping\n"))
	time.Sleep(100 * time.Millisecond)

	buf := make([]byte, 1024)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	assert.Equal(t, "tcp-plugin-output", strings.TrimSpace(string(buf[:n])))

	client.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for connection handler")
	}
}

func TestHandleTCPConnection_UnmatchedCommand(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	rex, _ := regexp.Compile("^ping$")
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		Commands: []parser.Command{
			{Regex: rex, Handler: "pong", Name: "ping-command"},
		},
	}
	strategy := newStrategyWithSessions()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTCPConnection(server, servConf, mt, strategy)
	}()

	client.Write([]byte("unknown\n"))
	client.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for connection handler")
	}

	// Should have: session start, unmatched interaction, session end
	assert.GreaterOrEqual(t, len(mt.events), 3)
	foundUnmatched := false
	foundEnd := false
	for _, e := range mt.events {
		if e.Handler == "not_found" {
			foundUnmatched = true
		}
		if e.Status == tracer.End.String() {
			foundEnd = true
		}
	}
	assert.True(t, foundUnmatched, "should have unmatched interaction event")
	assert.True(t, foundEnd, "should have end session event")
}

func TestHandleTCPConnection_PluginErrorAndExistingHistory(t *testing.T) {
	plugin.Register(&tcpErrorCommandPlugin{})
	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	rex := regexp.MustCompile("^ping$")
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		Commands:               []parser.Command{{Regex: rex, Plugin: "tcp-error-cmd", Handler: "fallback"}},
	}
	strategy := newStrategyWithSessions()
	strategy.Sessions.Append("TCP", plugins.Message{Role: plugins.USER.String(), Content: "old"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTCPConnection(server, servConf, mt, strategy)
	}()
	_, err := client.Write([]byte("ping\n"))
	require.NoError(t, err)
	buf := make([]byte, 64)
	_, err = client.Read(buf)
	require.NoError(t, err)
	client.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for handler")
	}
}

func TestHandleTCPConnection_MatchedEmptyOutput(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	servConf := parser.BeelzebubServiceConfiguration{
		DeadlineTimeoutSeconds: 5,
		Commands:               []parser.Command{{Regex: regexp.MustCompile("^noop$"), Name: "noop"}},
	}
	strategy := newStrategyWithSessions()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTCPConnection(server, servConf, &mockTracer{}, strategy)
	}()
	_, err := client.Write([]byte("noop\n"))
	require.NoError(t, err)
	client.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for handler")
	}
}

func TestHandleTCPConnection_WriteFailureAfterMatch(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	rex, _ := regexp.Compile("^ping$")
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		Commands: []parser.Command{
			{Regex: rex, Handler: "pong", Name: "ping-command"},
		},
	}
	strategy := newStrategyWithSessions()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTCPConnection(server, servConf, mt, strategy)
	}()

	// Send a matching command, read response
	client.Write([]byte("ping\n"))
	buf := make([]byte, 1024)
	_, err := client.Read(buf)
	require.NoError(t, err)

	// Now break the read side so the next write attempt fails
	// The handler will try to write the response and break out of the loop
	client.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for handler to exit on write failure")
	}

	// Should have session end event
	foundEnd := false
	for _, e := range mt.events {
		if e.Status == tracer.End.String() {
			foundEnd = true
			break
		}
	}
	assert.True(t, foundEnd)
}

func TestHandleTCPConnection_Deadline(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 1,
		Commands:               []parser.Command{},
	}
	strategy := newStrategyWithSessions()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTCPConnection(server, servConf, mt, strategy)
	}()

	// Don't write anything, let the deadline expire
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for deadline")
	}
}

func TestTCPStrategy_RegistryFactory(t *testing.T) {
	group := protocols.NewServiceGroupFromRegistry(func(tracer.Event) {})
	assert.NotNil(t, group.StrategyForProtocol("tcp"))
}
