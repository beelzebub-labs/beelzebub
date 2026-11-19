package TELNET

import (
	"context"
	"errors"
	"fmt"
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

type telnetTestCommandPlugin struct{}

func (p *telnetTestCommandPlugin) Metadata() plugin.Metadata {
	return plugin.Metadata{Name: "telnet-test-cmd"}
}

func (p *telnetTestCommandPlugin) Execute(_ context.Context, req plugin.CommandRequest) (string, error) {
	return "telnet-plugin-output", nil
}

type telnetErrorCommandPlugin struct{}

func (p *telnetErrorCommandPlugin) Metadata() plugin.Metadata {
	return plugin.Metadata{Name: "telnet-error-cmd"}
}

type telnetScriptedListener struct {
	accepted net.Conn
	step     int
}

type telnetScriptedAddr string

func (a telnetScriptedAddr) Network() string { return "tcp" }
func (a telnetScriptedAddr) String() string  { return string(a) }

func (l *telnetScriptedListener) Accept() (net.Conn, error) {
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

func (l *telnetScriptedListener) Close() error   { return nil }
func (l *telnetScriptedListener) Addr() net.Addr { return telnetScriptedAddr("telnet-scripted") }

func (p *telnetErrorCommandPlugin) Execute(context.Context, plugin.CommandRequest) (string, error) {
	return "", errors.New("telnet plugin failure")
}

type telnetPanicCommandPlugin struct{}

func (p *telnetPanicCommandPlugin) Metadata() plugin.Metadata {
	return plugin.Metadata{Name: "telnet-panic-cmd"}
}

func (p *telnetPanicCommandPlugin) Execute(context.Context, plugin.CommandRequest) (string, error) {
	panic("telnet handler panic")
}

type telnetCloseErrorListener struct{}

func (telnetCloseErrorListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (telnetCloseErrorListener) Close() error              { return errors.New("close failed") }
func (telnetCloseErrorListener) Addr() net.Addr            { return telnetScriptedAddr("close-error") }

type telnetFailWriteConn struct {
	net.Conn
	failAt int
	writes int
}

func (c *telnetFailWriteConn) Write(p []byte) (int, error) {
	c.writes++
	if c.writes == c.failAt {
		return 0, errors.New("write failed")
	}
	return c.Conn.Write(p)
}

type mockTracer struct {
	events []tracer.Event
}

func (m *mockTracer) TraceEvent(event tracer.Event) {
	m.events = append(m.events, event)
}

func newTelnetStrategy() *TelnetStrategy {
	return &TelnetStrategy{Sessions: historystore.NewHistoryStore()}
}

// drain reads from conn until deadline expires, discarding data.
func drain(conn net.Conn, timeout time.Duration) {
	conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 512)
	for {
		_, err := conn.Read(buf)
		if err != nil {
			break
		}
	}
	conn.SetReadDeadline(time.Time{})
}

func TestNegotiateTelnet_ReadTimeout(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		negotiateTelnet(server)
	}()

	// Don't send anything — let the 100ms deadline expire
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for negotiateTelnet")
	}
}

func TestNegotiateTelnet(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		negotiateTelnet(server)
	}()

	// Send a negotiation sequence quickly
	client.Write([]byte{IAC, WILL, ECHO})
	client.Close()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestReadLine(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	done := make(chan string, 1)
	go func() {
		s, _ := readLine(server)
		done <- s
	}()

	client.Write([]byte("hello\n"))
	select {
	case s := <-done:
		assert.Equal(t, "hello", s)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestReadLine_IACSkip(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	done := make(chan string, 1)
	go func() {
		s, _ := readLine(server)
		done <- s
	}()

	// Send IAC DO 1 followed by a newline
	client.Write([]byte{IAC, DO, ECHO, 'h', 'i', '\n'})
	select {
	case s := <-done:
		assert.Equal(t, "hi", s)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestReadLine_IACSubnegotiation(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	done := make(chan string, 1)
	go func() {
		s, _ := readLine(server)
		done <- s
	}()

	// Send IAC SB ... IAC SE followed by a newline
	msg := []byte{IAC, SB, 1, 2, 3, IAC, SE, 'o', 'k', '\n'}
	client.Write(msg)
	select {
	case s := <-done:
		assert.Equal(t, "ok", s)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestBuildPrompt(t *testing.T) {
	assert.Equal(t, "user@host:~$ ", buildPrompt("user", "host"))
	assert.Equal(t, "@:~$ ", buildPrompt("", ""))
}

func TestTelnetStrategy_Init_InvalidAddress(t *testing.T) {
	strategy := newTelnetStrategy()
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:     "invalid-address-no-port",
		Description: "test",
	}

	err := strategy.Init(servConf, mt)
	assert.Error(t, err)
}

func TestTelnetStrategy_Init_Valid(t *testing.T) {
	strategy := newTelnetStrategy()
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:       "127.0.0.1:0",
		Description:   "test telnet",
		PasswordRegex: ".*",
	}

	err := strategy.Init(servConf, mt)
	assert.NoError(t, err)
}

func TestHandleTelnetConnection_BadPassword(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	rex, _ := regexp.Compile("secret")
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		PasswordRegex:          rex.String(),
		ServerName:             "test",
	}
	strategy := newTelnetStrategy()

	go handleTelnetConnection(server, servConf, mt, strategy)

	drain(client, 500*time.Millisecond)

	client.Write([]byte("user\n"))
	drain(client, 100*time.Millisecond)
	client.Write([]byte("wrongpass\n"))

	time.Sleep(200 * time.Millisecond)
	client.Close()
}

func TestHandleTelnetConnection_ValidPassword(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	rex, _ := regexp.Compile(".*")
	cmdRex, _ := regexp.Compile("ls")
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		PasswordRegex:          rex.String(),
		ServerName:             "test",
		Commands: []parser.Command{
			{Regex: cmdRex, Handler: "ok"},
		},
	}
	strategy := newTelnetStrategy()

	go handleTelnetConnection(server, servConf, mt, strategy)

	drain(client, 500*time.Millisecond)
	client.Write([]byte("admin\n"))
	drain(client, 100*time.Millisecond)
	client.Write([]byte("mypass\n"))
	drain(client, 100*time.Millisecond)

	client.Write([]byte("ls\n"))

	time.Sleep(200 * time.Millisecond)
	client.Close()
}

func TestTelnetStrategy_StopAll(t *testing.T) {
	strategy := newTelnetStrategy()
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:       "127.0.0.1:0",
		Description:   "test",
		PasswordRegex: ".*",
	}

	assert.NoError(t, strategy.Init(servConf, mt))
	assert.Len(t, strategy.listeners, 1)

	assert.NoError(t, strategy.StopAll())
	assert.Nil(t, strategy.listeners)
}

func TestTelnetStrategy_StopAll_Empty(t *testing.T) {
	strategy := newTelnetStrategy()
	assert.NoError(t, strategy.StopAll())
}

func TestTelnetStrategy_StopAll_AlreadyClosed(t *testing.T) {
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	listener.Close()

	strategy := newTelnetStrategy()
	strategy.listeners = map[string]net.Listener{"test:0": listener}

	// Close on an already-closed listener returns an error now
	err := strategy.StopAll()
	assert.Error(t, err)
	assert.Nil(t, strategy.listeners)
}

func TestTelnetStrategy_Stop_WaitsOnlyForRequestedListener(t *testing.T) {
	strategy := &TelnetStrategy{}
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
			Protocol: "telnet",
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
		t.Fatal("stopping one TELNET listener waited for another listener")
	}

	assert.Len(t, strategy.listeners, 1)
}

func TestTelnetStrategy_Stop_CloseError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.New(t).NoError(err)
	addr := listener.Addr().String()
	listener.Close()

	listener2, err := net.Listen("tcp", addr)
	require.New(t).NoError(err)

	strategy := newTelnetStrategy()
	strategy.listeners = map[string]net.Listener{addr: listener2}

	err = strategy.Stop(parser.BeelzebubServiceConfiguration{Address: addr})
	assert.NoError(t, err)
	assert.Empty(t, strategy.listeners)
}

func TestTelnetStrategy_Stop_CloseErrorFromListener(t *testing.T) {
	strategy := newTelnetStrategy()
	strategy.listeners = map[string]net.Listener{"close-error": telnetCloseErrorListener{}}

	err := strategy.Stop(parser.BeelzebubServiceConfiguration{Address: "close-error"})
	assert.EqualError(t, err, "close failed")
}

func TestTelnetStrategy_Stop_ServerNotFound(t *testing.T) {
	strategy := newTelnetStrategy()
	strategy.listeners = map[string]net.Listener{"other:0": nil}

	err := strategy.Stop(parser.BeelzebubServiceConfiguration{Address: "test:0"})
	assert.NoError(t, err)
}

func TestTelnetStrategy_Init_OverwriteExisting(t *testing.T) {
	strategy := newTelnetStrategy()
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:       "127.0.0.1:0",
		PasswordRegex: ".*",
	}

	err := strategy.Init(servConf, mt)
	assert.NoError(t, err)
	assert.Len(t, strategy.listeners, 1)

	// Second Init with same address should close old listener and create new one
	err = strategy.Init(servConf, mt)
	assert.NoError(t, err)
	assert.Len(t, strategy.listeners, 1)

	strategy.StopAll()
}

func TestTelnetStrategy_Init_BadRegex(t *testing.T) {
	strategy := newTelnetStrategy()
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:       "127.0.0.1:0",
		PasswordRegex: "[invalid",
	}

	err := strategy.Init(servConf, mt)
	assert.NoError(t, err)
}

func TestTelnetStrategy_Init_OverwriteAndLazySessions(t *testing.T) {
	strategy := &TelnetStrategy{}
	config := parser.BeelzebubServiceConfiguration{Address: "127.0.0.1:0", Protocol: "telnet", PasswordRegex: ".*"}
	require.NoError(t, strategy.Init(config, &mockTracer{}))
	require.NoError(t, strategy.Init(config, &mockTracer{}))
	require.NoError(t, strategy.StopAll())
}

func TestTelnetStrategy_Init_AcceptErrorAndAcceptedConnection(t *testing.T) {
	client, server := net.Pipe()
	client.Close()
	oldListen := listenTCP
	listenTCP = func(string, string) (net.Listener, error) {
		return &telnetScriptedListener{accepted: server}, nil
	}
	t.Cleanup(func() { listenTCP = oldListen })

	strategy := &TelnetStrategy{}
	require.NoError(t, strategy.Init(parser.BeelzebubServiceConfiguration{Address: "scripted", PasswordRegex: ".*"}, &mockTracer{}))
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, strategy.StopAll())
}

func TestTelnetStrategy_Init_RecoversHandlerPanic(t *testing.T) {
	plugin.Register(&telnetPanicCommandPlugin{})
	client, server := net.Pipe()
	defer client.Close()

	oldListen := listenTCP
	listenTCP = func(string, string) (net.Listener, error) {
		return &telnetScriptedListener{accepted: server}, nil
	}
	t.Cleanup(func() { listenTCP = oldListen })

	strategy := &TelnetStrategy{}
	require.NoError(t, strategy.Init(parser.BeelzebubServiceConfiguration{
		Address: "panic", PasswordRegex: ".*",
		Commands: []parser.Command{{Regex: regexp.MustCompile("^boom$"), Plugin: "telnet-panic-cmd"}},
	}, &mockTracer{}))
	drain(client, 500*time.Millisecond)
	_, _ = client.Write([]byte("admin\n"))
	drain(client, 100*time.Millisecond)
	_, _ = client.Write([]byte("pass\n"))
	drain(client, 100*time.Millisecond)
	_, _ = client.Write([]byte("boom\n"))
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, strategy.StopAll())
}

func TestTelnetStrategy_RegistryFactory(t *testing.T) {
	group := protocols.NewServiceGroupFromRegistry(func(tracer.Event) {})
	assert.NotNil(t, group.StrategyForProtocol("telnet"))
}

func TestReadLine_ControlChars(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	done := make(chan string, 1)
	go func() {
		s, _ := readLine(server)
		done <- s
	}()

	// Control chars (e.g. 0x01) should be skipped
	client.Write([]byte{0x01, 0x02, 'a', 'b', '\n'})
	select {
	case s := <-done:
		assert.Equal(t, "ab", s)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestReadLine_EOF(t *testing.T) {
	client, server := net.Pipe()
	client.Close()

	_, err := readLine(server)
	assert.Error(t, err)
}

func TestReadLine_IACSubnegotiation_Incomplete(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	done := make(chan string, 1)
	go func() {
		s, _ := readLine(server)
		done <- s
	}()

	// Send IAC SB with data followed by IAC SE to properly close, then newline
	client.Write([]byte{IAC, SB, 1, 2, 3, IAC, SE, 'x', '\n'})
	select {
	case s := <-done:
		assert.Equal(t, "x", s)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestHandleTelnetConnection_UnmatchedCommand(t *testing.T) {
	client, server := net.Pipe()
	mt := &mockTracer{}
	rex, _ := regexp.Compile("^ls$")
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		PasswordRegex:          ".*",
		ServerName:             "test",
		Commands: []parser.Command{
			{Regex: rex, Handler: "ok"},
		},
	}
	strategy := newTelnetStrategy()

	done := make(chan struct{})
	go func() {
		handleTelnetConnection(server, servConf, mt, strategy)
		close(done)
	}()

	drain(client, 500*time.Millisecond)
	client.Write([]byte("admin\n"))
	drain(client, 100*time.Millisecond)
	client.Write([]byte("pass\n"))
	drain(client, 100*time.Millisecond)

	// Send unmatched command
	client.Write([]byte("unknown\n"))
	time.Sleep(200 * time.Millisecond)

	buf := make([]byte, 512)
	n, _ := client.Read(buf)
	assert.Contains(t, string(buf[:n]), "command not found")

	client.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for handler")
	}

	// Verify session end trace (safe to read after handler exited)
	foundEnd := false
	for _, e := range mt.events {
		if e.Msg == "End TELNET Session" {
			foundEnd = true
			break
		}
	}
	assert.True(t, foundEnd, "should have end session trace event")
}

func TestHandleTelnetConnection_PromptWriteFailure(t *testing.T) {
	client, server := net.Pipe()
	mt := &mockTracer{}
	rex, _ := regexp.Compile("^ls$")
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		PasswordRegex:          ".*",
		ServerName:             "test",
		Commands: []parser.Command{
			{Regex: rex, Handler: "ok"},
		},
	}
	strategy := newTelnetStrategy()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTelnetConnection(server, servConf, mt, strategy)
	}()

	// Drain the initial login prompts
	drain(client, 500*time.Millisecond)

	// Send username
	client.Write([]byte("admin\n"))
	drain(client, 100*time.Millisecond)
	// Send password — after this, the handler enters the command loop
	client.Write([]byte("pass\n"))
	time.Sleep(200 * time.Millisecond)

	// Close the write side to make the next prompt write fail
	client.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for handler to exit on write failure")
	}
}

func TestHandleTelnetConnection_WithPluginCommand(t *testing.T) {
	plugin.Register(&telnetTestCommandPlugin{})

	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	rex, _ := regexp.Compile("ls")
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		PasswordRegex:          ".*",
		ServerName:             "test",
		Commands: []parser.Command{
			{
				Regex:  rex,
				Plugin: "telnet-test-cmd",
			},
		},
	}
	strategy := newTelnetStrategy()

	go handleTelnetConnection(server, servConf, mt, strategy)

	drain(client, 500*time.Millisecond)
	client.Write([]byte("admin\n"))
	drain(client, 100*time.Millisecond)
	client.Write([]byte("pass\n"))
	drain(client, 100*time.Millisecond)

	client.Write([]byte("ls\n"))
	time.Sleep(200 * time.Millisecond)

	buf := make([]byte, 512)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("failed to read plugin response: %v", err)
	}
	output := strings.TrimSpace(string(buf[:n]))
	assert.Contains(t, output, "telnet-plugin-output")

	client.Close()
}

func TestHandleTelnetConnection_PluginError(t *testing.T) {
	plugin.Register(&telnetErrorCommandPlugin{})
	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		PasswordRegex:          ".*",
		ServerName:             "test",
		Commands:               []parser.Command{{Regex: regexp.MustCompile("^ls$"), Plugin: "telnet-error-cmd", Handler: "fallback"}},
	}
	strategy := newTelnetStrategy()
	strategy.Sessions.Append("TELNETadmin", plugins.Message{Role: plugins.USER.String(), Content: "old"})
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTelnetConnection(server, servConf, mt, strategy)
	}()
	drain(client, 500*time.Millisecond)
	_, err := client.Write([]byte("admin\n"))
	require.NoError(t, err)
	drain(client, 100*time.Millisecond)
	_, err = client.Write([]byte("pass\n"))
	require.NoError(t, err)
	drain(client, 100*time.Millisecond)
	_, err = client.Write([]byte("ls\n"))
	require.NoError(t, err)
	buf := make([]byte, 128)
	_, err = client.Read(buf)
	require.NoError(t, err)
	client.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for handler")
	}
}

func TestHandleTelnetConnection_LoginPhaseReadAndWriteFailures(t *testing.T) {
	t.Run("username read", func(t *testing.T) {
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			handleTelnetConnection(server, parser.BeelzebubServiceConfiguration{PasswordRegex: ".*"}, &mockTracer{}, newTelnetStrategy())
		}()
		drain(client, 500*time.Millisecond)
		client.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timeout")
		}
	})

	t.Run("write after username", func(t *testing.T) {
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			handleTelnetConnection(server, parser.BeelzebubServiceConfiguration{PasswordRegex: ".*"}, &mockTracer{}, newTelnetStrategy())
		}()
		drain(client, 500*time.Millisecond)
		_, _ = client.Write([]byte("admin\n"))
		client.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timeout")
		}
	})

	t.Run("password read", func(t *testing.T) {
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			handleTelnetConnection(server, parser.BeelzebubServiceConfiguration{PasswordRegex: ".*"}, &mockTracer{}, newTelnetStrategy())
		}()
		drain(client, 500*time.Millisecond)
		_, _ = client.Write([]byte("admin\n"))
		drain(client, 100*time.Millisecond)
		client.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timeout")
		}
	})
}

func TestHandleTelnetConnection_WriteFailAtLogin(t *testing.T) {
	// Create a conn that fails immediately on write
	client, server := net.Pipe()
	client.Close()

	mt := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{
		Description:   "test",
		PasswordRegex: ".*",
	}
	strategy := newTelnetStrategy()

	// Should not panic, just return
	handleTelnetConnection(server, servConf, mt, strategy)
}

func TestHandleTelnetConnection_WritePasswordFailure(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	wrapped := &telnetFailWriteConn{Conn: server, failAt: 3}
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTelnetConnection(wrapped, parser.BeelzebubServiceConfiguration{PasswordRegex: ".*"}, &mockTracer{}, newTelnetStrategy())
	}()
	drain(client, 500*time.Millisecond)
	_, _ = client.Write([]byte("admin\n"))
	drain(client, 100*time.Millisecond)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for password write failure")
	}
}

func TestHandleTelnetConnection_InvalidPasswordRegex(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTelnetConnection(server, parser.BeelzebubServiceConfiguration{PasswordRegex: "["}, &mockTracer{}, newTelnetStrategy())
	}()
	drain(client, 500*time.Millisecond)
	_, _ = client.Write([]byte("admin\n"))
	drain(client, 100*time.Millisecond)
	_, _ = client.Write([]byte("pass\n"))
	drain(client, 100*time.Millisecond)
	client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for invalid regex")
	}
}

func TestHandleTelnetConnection_ExitAndUnknownPlugin(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	strategy := newTelnetStrategy()
	conf := parser.BeelzebubServiceConfiguration{
		PasswordRegex: ".*",
		Commands:      []parser.Command{{Regex: regexp.MustCompile("^unknown$"), Plugin: "missing-plugin", Handler: "fallback"}},
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTelnetConnection(server, conf, &mockTracer{}, strategy)
	}()
	drain(client, 500*time.Millisecond)
	_, _ = client.Write([]byte("admin\n"))
	drain(client, 100*time.Millisecond)
	_, _ = client.Write([]byte("pass\n"))
	drain(client, 100*time.Millisecond)
	_, _ = client.Write([]byte("unknown\n"))
	drain(client, 100*time.Millisecond)
	_, _ = client.Write([]byte("exit\n"))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for exit")
	}
}

func TestHandleTelnetConnection_UnmatchedCommandWriteFailure(t *testing.T) {
	client, server := net.Pipe()
	wrapped := &telnetFailWriteConn{Conn: server, failAt: 6}
	conf := parser.BeelzebubServiceConfiguration{PasswordRegex: ".*"}
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTelnetConnection(wrapped, conf, &mockTracer{}, newTelnetStrategy())
	}()
	drain(client, 500*time.Millisecond)
	_, _ = client.Write([]byte("admin\n"))
	drain(client, 100*time.Millisecond)
	_, _ = client.Write([]byte("pass\n"))
	drain(client, 100*time.Millisecond)
	_, _ = client.Write([]byte("unknown\n"))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for unmatched write failure")
	}
	client.Close()
}

func TestReadLine_TruncatedTelnetSequences(t *testing.T) {
	tests := [][]byte{{IAC}, {IAC, SB, 1}, {IAC, SB, 1, IAC}, {IAC, WILL}}
	for _, input := range tests {
		t.Run(fmt.Sprintf("%x", input), func(t *testing.T) {
			client, server := net.Pipe()
			done := make(chan error, 1)
			go func() {
				_, err := readLine(server)
				done <- err
			}()
			_, _ = client.Write(input)
			client.Close()
			select {
			case err := <-done:
				assert.Error(t, err)
			case <-time.After(time.Second):
				t.Fatal("timeout")
			}
		})
	}
}
