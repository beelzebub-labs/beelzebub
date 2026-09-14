package SSH

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/historystore"
	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
	"github.com/gliderlabs/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gocrypto_ssh "golang.org/x/crypto/ssh"
)

type sshTestCommandPlugin struct{}

func (p *sshTestCommandPlugin) Metadata() plugin.Metadata {
	return plugin.Metadata{Name: "ssh-test-cmd"}
}

func (p *sshTestCommandPlugin) Execute(_ context.Context, req plugin.CommandRequest) (string, error) {
	return "ssh-plugin-output", nil
}

type sshTestCommandPluginError struct{}

func (p *sshTestCommandPluginError) Metadata() plugin.Metadata {
	return plugin.Metadata{Name: "ssh-test-cmd-err"}
}

func (p *sshTestCommandPluginError) Execute(_ context.Context, req plugin.CommandRequest) (string, error) {
	return "", fmt.Errorf("simulated error")
}

type mockTracer struct {
	events []tracer.Event
}

func (m *mockTracer) TraceEvent(event tracer.Event) {
	m.events = append(m.events, event)
}

func TestBuildPrompt(t *testing.T) {
	tests := []struct {
		user       string
		serverName string
		expected   string
	}{
		{"root", "ubuntu", "root@ubuntu:~$ "},
		{"admin", "debian", "admin@debian:~$ "},
		{"", "", "@:~$ "},
		{"user", "", "user@:~$ "},
		{"", "server", "@server:~$ "},
	}
	for _, tt := range tests {
		t.Run(tt.user+"@"+tt.serverName, func(t *testing.T) {
			assert.Equal(t, tt.expected, buildPrompt(tt.user, tt.serverName))
		})
	}
}

func TestMain(m *testing.M) {
	plugin.Cleanup()
	plugin.Register(&sshTestCommandPlugin{})
	plugin.Register(&sshTestCommandPluginError{})
	plugin.Register(&sshTestTerminalPlugin{})
	plugin.Register(&sshTestTerminalPluginError{})

	m.Run()
}

func TestSSHStrategy_Init_ValidAddress(t *testing.T) {
	strategy := &SSHStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:                "127.0.0.1:0",
		Description:            "test SSH",
		DeadlineTimeoutSeconds: 2,
		PasswordRegex:          ".*",
	}

	err := strategy.Init(servConf, mt)
	assert.NoError(t, err)
	assert.NotNil(t, strategy.Sessions)
}

func TestSSHStrategy_Init_ReusesExistingSessions(t *testing.T) {
	strategy := &SSHStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:                "127.0.0.1:0",
		DeadlineTimeoutSeconds: 1,
		PasswordRegex:          ".*",
	}

	assert.NoError(t, strategy.Init(servConf, mt))
	assert.NotNil(t, strategy.Sessions)

	original := strategy.Sessions

	// A second Init must reuse the same Sessions store, not replace it.
	assert.NoError(t, strategy.Init(servConf, mt))
	assert.Same(t, original, strategy.Sessions)
}

func TestSSHStrategy_Init_InvalidAddress(t *testing.T) {
	strategy := &SSHStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:       "invalid-address-no-port",
		PasswordRegex: ".*",
	}

	// Listener setup is synchronous so invalid addresses are reported by Init.
	assert.Error(t, strategy.Init(servConf, mt))
}

func TestSSHStrategy_StopAll(t *testing.T) {
	strategy := &SSHStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:                "127.0.0.1:0",
		Description:            "test SSH",
		DeadlineTimeoutSeconds: 2,
		PasswordRegex:          ".*",
	}

	assert.NoError(t, strategy.Init(servConf, mt))
	assert.Equal(t, 1, len(strategy.servers))

	assert.NoError(t, strategy.StopAll())
	assert.Nil(t, strategy.servers)
}

func TestSSHStrategy_StopAll_Empty(t *testing.T) {
	strategy := &SSHStrategy{}
	assert.NoError(t, strategy.StopAll())
}

func TestSSHStrategy_StopAll_MultipleInits(t *testing.T) {
	strategy := &SSHStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:                "127.0.0.1:0",
		Description:            "test SSH",
		DeadlineTimeoutSeconds: 1,
		PasswordRegex:          ".*",
	}

	assert.NoError(t, strategy.Init(servConf, mt))
	assert.NoError(t, strategy.Init(servConf, mt))
	assert.Equal(t, 1, len(strategy.servers))

	assert.NoError(t, strategy.StopAll())
	assert.Nil(t, strategy.servers)

	// Verify servers can be restarted after StopAll
	assert.NoError(t, strategy.Init(servConf, mt))
	assert.Equal(t, 1, len(strategy.servers))

	time.Sleep(100 * time.Millisecond)
	assert.NoError(t, strategy.StopAll())
}

type failOnCloseListener struct {
	net.Listener
	closeErr error
}

func (f *failOnCloseListener) Close() error {
	f.Listener.Close()
	return f.closeErr
}

type mockSession struct {
	rawCmd     string
	user       string
	remoteAddr string
	environ    []string
	writeBuf   []byte
	readBuf    []byte
}

func (m *mockSession) Write(p []byte) (int, error) {
	m.writeBuf = append(m.writeBuf, p...)
	return len(p), nil
}

func (m *mockSession) Read(p []byte) (int, error) {
	if len(m.readBuf) == 0 {
		return 0, fmt.Errorf("EOF")
	}
	n := copy(p, m.readBuf)
	m.readBuf = m.readBuf[n:]
	return n, nil
}

func (m *mockSession) User() string { return m.user }
func (m *mockSession) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 2222}
}
func (m *mockSession) Environ() []string                              { return m.environ }
func (m *mockSession) RawCommand() string                             { return m.rawCmd }
func (m *mockSession) Exit(code int) error                            { return nil }
func (m *mockSession) Command() []string                              { return nil }
func (m *mockSession) Subsystem() string                              { return "" }
func (m *mockSession) PublicKey() ssh.PublicKey                       { return nil }
func (m *mockSession) Context() ssh.Context                           { return &mockContext{user: m.user} }
func (m *mockSession) Permissions() ssh.Permissions                   { return ssh.Permissions{} }
func (m *mockSession) Pty() (ssh.Pty, <-chan ssh.Window, bool)        { return ssh.Pty{}, nil, false }
func (m *mockSession) Signer() ssh.Signer                             { return nil }
func (m *mockSession) Close() error                                   { return nil }
func (m *mockSession) CloseWrite() error                              { return nil }
func (m *mockSession) Break(chan<- bool)                              {}
func (m *mockSession) LocalAddr() net.Addr                            { return &net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: 22} }
func (m *mockSession) Lock()                                          {}
func (m *mockSession) Unlock()                                        {}
func (m *mockSession) SendRequest(string, bool, []byte) (bool, error) { return true, nil }
func (m *mockSession) Stderr() io.ReadWriter                          { return nil }
func (m *mockSession) Signals(ch chan<- ssh.Signal)                   {}

type mockContext struct {
	user string
}

func (m *mockContext) User() string { return m.user }
func (m *mockContext) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 2222}
}
func (m *mockContext) Environ() []string                 { return nil }
func (m *mockContext) Permissions() *ssh.Permissions     { return nil }
func (m *mockContext) SetValue(k, v interface{})         {}
func (m *mockContext) ClientVersion() string             { return "SSH-2.0-test" }
func (m *mockContext) ServerVersion() string             { return "SSH-2.0-beelzebub" }
func (m *mockContext) Deadline() (time.Time, bool)       { return time.Time{}, false }
func (m *mockContext) Done() <-chan struct{}             { return nil }
func (m *mockContext) Err() error                        { return nil }
func (m *mockContext) Value(key interface{}) interface{} { return nil }
func (m *mockContext) LocalAddr() net.Addr               { return &net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: 22} }
func (m *mockContext) SessionID() string                 { return "test-session-id" }
func (m *mockContext) Lock()                             {}
func (m *mockContext) Unlock()                           {}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().(*net.TCPAddr)
	l.Close()
	return addr.Port
}

func dialSSHWithRetry(t *testing.T, addr string, config *gocrypto_ssh.ClientConfig) *gocrypto_ssh.Client {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := gocrypto_ssh.Dial("tcp", addr, config)
		if err == nil {
			return conn
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.FailNow(t, "timed out waiting for SSH server at "+addr)
	return nil
}

func waitForServer(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestHandleSession_RawCommand(t *testing.T) {
	mt := &mockTracer{}
	sessions := historystore.NewHistoryStore()
	rex := regexp.MustCompile("^ls$")
	servConf := parser.BeelzebubServiceConfiguration{
		Description: "test",
		Commands: []parser.Command{
			{Regex: rex, Handler: "bin  boot  dev", Name: "ls-cmd"},
		},
	}
	sess := &mockSession{rawCmd: "ls", user: "root"}

	handleSession(sess, servConf, mt, sessions)

	assert.Contains(t, string(sess.writeBuf), "bin")
	assert.GreaterOrEqual(t, len(mt.events), 1)
	assert.Equal(t, "SSH Raw Command", mt.events[0].Msg)
}

func TestHandleSession_RawCommand_NotFound(t *testing.T) {
	mt := &mockTracer{}
	sessions := historystore.NewHistoryStore()
	rex := regexp.MustCompile("^ls$")
	servConf := parser.BeelzebubServiceConfiguration{
		Description: "test",
		ServerName:  "test-server",
		Commands: []parser.Command{
			{Regex: rex, Handler: "bin  boot", Name: "ls-cmd"},
		},
	}
	sess := &mockSession{rawCmd: "unknown", user: "root"}

	handleSession(sess, servConf, mt, sessions)

	// Should fall through to terminal session
	assert.GreaterOrEqual(t, len(mt.events), 2)
	assert.Equal(t, "New SSH Terminal Session", mt.events[0].Msg)
}

func TestHandleSession_RawCommand_WithExit(t *testing.T) {
	mt := &mockTracer{}
	sessions := historystore.NewHistoryStore()
	rex := regexp.MustCompile("^exit$")
	servConf := parser.BeelzebubServiceConfiguration{
		Description: "test",
		ServerName:  "test-server",
		Commands: []parser.Command{
			{Regex: rex, Handler: "", Name: "exit-cmd"},
		},
	}
	sess := &mockSession{rawCmd: "exit", user: "root"}

	handleSession(sess, servConf, mt, sessions)

	assert.GreaterOrEqual(t, len(mt.events), 1)
	assert.Equal(t, "SSH Raw Command", mt.events[0].Msg)
}

func TestHandlePassword_Match(t *testing.T) {
	mt := &mockTracer{}
	ctx := &mockContext{user: "root"}
	servConf := parser.BeelzebubServiceConfiguration{
		PasswordRegex: "^root|admin$",
	}

	result := handlePassword(ctx, "root", servConf, mt)
	assert.True(t, result)

	assert.GreaterOrEqual(t, len(mt.events), 1)
	assert.Equal(t, "New SSH Login Attempt", mt.events[0].Msg)
}

func TestHandlePassword_NoMatch(t *testing.T) {
	mt := &mockTracer{}
	ctx := &mockContext{user: "root"}
	servConf := parser.BeelzebubServiceConfiguration{
		PasswordRegex: "^admin$",
	}

	result := handlePassword(ctx, "wrongpass", servConf, mt)
	assert.False(t, result)
}

func TestHandlePassword_InvalidRegex(t *testing.T) {
	mt := &mockTracer{}
	ctx := &mockContext{user: "root"}
	servConf := parser.BeelzebubServiceConfiguration{
		PasswordRegex: "[invalid",
	}

	result := handlePassword(ctx, "test", servConf, mt)
	assert.False(t, result)
}

func TestHandleSession_PluginCommand(t *testing.T) {
	mt := &mockTracer{}
	sessions := historystore.NewHistoryStore()
	rex := regexp.MustCompile("^ping$")
	servConf := parser.BeelzebubServiceConfiguration{
		Description: "test",
		Commands: []parser.Command{
			{Regex: rex, Plugin: "ssh-test-cmd", Name: "ping-cmd"},
		},
	}
	sess := &mockSession{rawCmd: "ping", user: "root"}

	handleSession(sess, servConf, mt, sessions)

	assert.Contains(t, string(sess.writeBuf), "ssh-plugin-output")
	assert.GreaterOrEqual(t, len(mt.events), 1)
}

func TestHandleSession_PluginCommandError(t *testing.T) {
	mt := &mockTracer{}
	sessions := historystore.NewHistoryStore()
	rex := regexp.MustCompile("^fail$")
	servConf := parser.BeelzebubServiceConfiguration{
		Description: "test",
		Commands: []parser.Command{
			{Regex: rex, Plugin: "ssh-test-cmd-err", Name: "fail-cmd"},
		},
	}
	sess := &mockSession{rawCmd: "fail", user: "root"}

	handleSession(sess, servConf, mt, sessions)

	assert.Contains(t, string(sess.writeBuf), "command not found")
}

func TestHandleSession_TerminalCommand(t *testing.T) {
	mt := &mockTracer{}
	sessions := historystore.NewHistoryStore()
	rex := regexp.MustCompile("^ls$")
	servConf := parser.BeelzebubServiceConfiguration{
		Description: "test",
		ServerName:  "test-server",
		Commands: []parser.Command{
			{Regex: rex, Handler: "bin  boot", Name: "ls-cmd"},
		},
	}
	sess := &mockSession{
		rawCmd:  "",
		user:    "root",
		readBuf: []byte("ls\nexit\n"),
	}

	handleSession(sess, servConf, mt, sessions)

	// Should have terminal interaction and end session events
	foundInteraction := false
	foundEnd := false
	for _, e := range mt.events {
		if e.Msg == "SSH Terminal Session Interaction" {
			foundInteraction = true
		}
		if e.Msg == "End SSH Session" {
			foundEnd = true
		}
	}
	assert.True(t, foundInteraction, "should have terminal interaction event")
	assert.True(t, foundEnd, "should have end session event")
}

func TestHandleSession_TerminalUnknownCommand(t *testing.T) {
	mt := &mockTracer{}
	sessions := historystore.NewHistoryStore()
	rex := regexp.MustCompile("^ls$")
	servConf := parser.BeelzebubServiceConfiguration{
		Description: "test",
		ServerName:  "test-server",
		Commands: []parser.Command{
			{Regex: rex, Handler: "bin", Name: "ls-cmd"},
		},
	}
	sess := &mockSession{
		rawCmd:  "",
		user:    "root",
		readBuf: []byte("unknown\nexit\n"),
	}

	handleSession(sess, servConf, mt, sessions)

	// Unknown command just loops back, no interaction event
	foundInteraction := false
	for _, e := range mt.events {
		if e.Msg == "SSH Terminal Session Interaction" {
			foundInteraction = true
			break
		}
	}
	assert.False(t, foundInteraction, "should NOT have interaction event for unknown command")
}

func TestHandleSession_EndSession(t *testing.T) {
	mt := &mockTracer{}
	sessions := historystore.NewHistoryStore()
	servConf := parser.BeelzebubServiceConfiguration{
		Description: "test",
		ServerName:  "test-server",
	}
	sess := &mockSession{rawCmd: "", user: "root"}

	handleSession(sess, servConf, mt, sessions)

	// No raw command → enters terminal loop → Read returns EOF → exits → end session trace
	foundEnd := false
	for _, e := range mt.events {
		if e.Msg == "End SSH Session" {
			foundEnd = true
			break
		}
	}
	assert.True(t, foundEnd, "should emit End SSH Session event")
}

func TestSSHStrategy_Stop_ServerNotFound(t *testing.T) {
	strategy := &SSHStrategy{}
	strategy.servers = map[string]*ssh.Server{"other:0": nil}

	err := strategy.Stop(parser.BeelzebubServiceConfiguration{Address: "test:0"})
	assert.NoError(t, err)
}

func TestSSHStrategy_StopAll_ShutdownError(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	failL := &failOnCloseListener{Listener: l, closeErr: errors.New("simulated close error")}

	server := &ssh.Server{
		Handler: ssh.Handler(func(s ssh.Session) {}),
	}
	ready := make(chan struct{}, 1)
	readyOnce := &sync.Once{}
	go func() {
		server.Serve(&readyListener{Listener: failL, ready: ready, readyOnce: readyOnce})
	}()
	<-ready

	strategy := &SSHStrategy{}
	strategy.servers = map[string]*ssh.Server{"test:0": server}

	err = strategy.StopAll()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "simulated close error")
	assert.Nil(t, strategy.servers)
}

type sshTestTerminalPlugin struct{}

func (p *sshTestTerminalPlugin) Metadata() plugin.Metadata {
	return plugin.Metadata{Name: "ssh-test-term"}
}

func (p *sshTestTerminalPlugin) Execute(_ context.Context, req plugin.CommandRequest) (string, error) {
	return "terminal-plugin-output", nil
}

type sshTestTerminalPluginError struct{}

func (p *sshTestTerminalPluginError) Metadata() plugin.Metadata {
	return plugin.Metadata{Name: "ssh-test-term-err"}
}

func (p *sshTestTerminalPluginError) Execute(_ context.Context, req plugin.CommandRequest) (string, error) {
	return "", fmt.Errorf("terminal plugin error")
}

func TestHandleSession_TerminalWithPlugin(t *testing.T) {
	mt := &mockTracer{}
	sessions := historystore.NewHistoryStore()
	rex := regexp.MustCompile("^ping$")
	servConf := parser.BeelzebubServiceConfiguration{
		Description: "test",
		ServerName:  "test-server",
		Commands: []parser.Command{
			{Regex: rex, Plugin: "ssh-test-term", Name: "ping-cmd"},
		},
	}
	sess := &mockSession{
		rawCmd:  "",
		user:    "root",
		readBuf: []byte("ping\nexit\n"),
	}

	handleSession(sess, servConf, mt, sessions)

	assert.Contains(t, string(sess.writeBuf), "terminal-plugin-output")
	foundInteraction := false
	for _, e := range mt.events {
		if e.Msg == "SSH Terminal Session Interaction" {
			foundInteraction = true
			assert.Equal(t, "ping", e.Command)
			assert.Equal(t, "terminal-plugin-output", e.CommandOutput)
			assert.Equal(t, "ping-cmd", e.Handler)
			break
		}
	}
	assert.True(t, foundInteraction, "should have terminal interaction event")
}

func TestHandleSession_TerminalWithPluginError(t *testing.T) {
	mt := &mockTracer{}
	sessions := historystore.NewHistoryStore()
	rex := regexp.MustCompile("^fail$")
	servConf := parser.BeelzebubServiceConfiguration{
		Description: "test",
		ServerName:  "test-server",
		Commands: []parser.Command{
			{Regex: rex, Plugin: "ssh-test-term-err", Name: "fail-cmd"},
		},
	}
	sess := &mockSession{
		rawCmd:  "",
		user:    "root",
		readBuf: []byte("fail\nexit\n"),
	}

	handleSession(sess, servConf, mt, sessions)

	assert.Contains(t, string(sess.writeBuf), "command not found")
}

func TestHandleSession_TerminalWithUnknownPlugin(t *testing.T) {
	mt := &mockTracer{}
	sessions := historystore.NewHistoryStore()
	rex := regexp.MustCompile("^unknown$")
	servConf := parser.BeelzebubServiceConfiguration{
		Description: "test",
		ServerName:  "test-server",
		Commands: []parser.Command{
			{Regex: rex, Plugin: "non-existent-plugin", Name: "unknown-cmd"},
		},
	}
	sess := &mockSession{
		rawCmd:  "",
		user:    "root",
		readBuf: []byte("unknown\nexit\n"),
	}

	handleSession(sess, servConf, mt, sessions)

	foundEnd := false
	for _, e := range mt.events {
		if e.Msg == "End SSH Session" {
			foundEnd = true
			break
		}
	}
	assert.True(t, foundEnd, "should end session cleanly despite unknown plugin")
}

func TestHandleSession_TerminalWithHistory(t *testing.T) {
	mt := &mockTracer{}
	sessions := historystore.NewHistoryStore()
	sessionKey := "SSH10.0.0.1root"
	sessions.Append(sessionKey, plugins.Message{Role: plugins.USER.String(), Content: "previous"}, plugins.Message{Role: plugins.ASSISTANT.String(), Content: "output"})

	rex := regexp.MustCompile("^history$")
	servConf := parser.BeelzebubServiceConfiguration{
		Description: "test",
		ServerName:  "test-server",
		Commands: []parser.Command{
			{Regex: rex, Handler: "history-output", Name: "history-cmd"},
		},
	}
	sess := &mockSession{
		rawCmd:  "",
		user:    "root",
		readBuf: []byte("history\nexit\n"),
	}

	handleSession(sess, servConf, mt, sessions)

	assert.Contains(t, string(sess.writeBuf), "history-output")
}

func TestHandleSession_TerminalRawCommandWithHistory(t *testing.T) {
	mt := &mockTracer{}
	sessions := historystore.NewHistoryStore()
	sessionKey := "SSH10.0.0.1root"
	sessions.Append(sessionKey, plugins.Message{Role: plugins.USER.String(), Content: "previous"}, plugins.Message{Role: plugins.ASSISTANT.String(), Content: "output"})

	rex := regexp.MustCompile("^ls$")
	servConf := parser.BeelzebubServiceConfiguration{
		Description: "test",
		Commands: []parser.Command{
			{Regex: rex, Handler: "bin  boot  dev", Name: "ls-cmd"},
		},
	}
	sess := &mockSession{rawCmd: "ls", user: "root"}

	handleSession(sess, servConf, mt, sessions)

	assert.Contains(t, string(sess.writeBuf), "bin")
	assert.GreaterOrEqual(t, len(mt.events), 1)
	assert.Equal(t, "SSH Raw Command", mt.events[0].Msg)
}

func TestSSHStrategy_Stop_ExistingServer(t *testing.T) {
	strategy := &SSHStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:                "127.0.0.1:0",
		Description:            "test SSH",
		DeadlineTimeoutSeconds: 2,
		PasswordRegex:          ".*",
	}

	assert.NoError(t, strategy.Init(servConf, mt))
	assert.Equal(t, 1, len(strategy.servers))

	err := strategy.Stop(servConf)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(strategy.servers))
}

func TestSSHStrategy_Init_HandlerAndPasswordHandlerInvoked(t *testing.T) {
	port := freePort(t)

	strategy := &SSHStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:                fmt.Sprintf("127.0.0.1:%d", port),
		Description:            "test SSH",
		DeadlineTimeoutSeconds: 2,
		PasswordRegex:          ".*",
		ServerVersion:          "SSH-2.0-test",
	}

	err := strategy.Init(servConf, mt)
	require.NoError(t, err)

	server := strategy.servers[servConf.Address]
	require.NotNil(t, server)

	clientConfig := &gocrypto_ssh.ClientConfig{
		User: "testuser",
		Auth: []gocrypto_ssh.AuthMethod{
			gocrypto_ssh.Password("anypassword"),
		},
		HostKeyCallback: gocrypto_ssh.InsecureIgnoreHostKey(),
	}

	conn := dialSSHWithRetry(t, server.Addr, clientConfig)
	defer conn.Close()

	session, err := conn.NewSession()
	require.NoError(t, err)
	defer session.Close()

	err = session.Run("echo test")
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	foundLogin := false
	foundTerminal := false
	for _, e := range mt.events {
		if e.Msg == "New SSH Login Attempt" {
			foundLogin = true
			assert.Equal(t, "testuser", e.User)
			assert.Equal(t, "anypassword", e.Password)
		}
		if e.Msg == "New SSH Terminal Session" {
			foundTerminal = true
		}
	}
	assert.True(t, foundLogin, "PasswordHandler should have been invoked")
	assert.True(t, foundTerminal, "Handler should have been invoked")
	assert.NoError(t, strategy.StopAll())
}

func TestSSHStrategy_Init_TerminalSession(t *testing.T) {
	port := freePort(t)

	strategy := &SSHStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:                fmt.Sprintf("127.0.0.1:%d", port),
		Description:            "test SSH",
		DeadlineTimeoutSeconds: 2,
		PasswordRegex:          ".*",
		ServerName:             "test-server",
		ServerVersion:          "SSH-2.0-test",
		Commands: []parser.Command{
			{Regex: regexp.MustCompile("^ping$"), Plugin: "ssh-test-term", Name: "ping-cmd"},
		},
	}

	err := strategy.Init(servConf, mt)
	require.NoError(t, err)

	server := strategy.servers[servConf.Address]
	require.NotNil(t, server)

	clientConfig := &gocrypto_ssh.ClientConfig{
		User: "testuser",
		Auth: []gocrypto_ssh.AuthMethod{
			gocrypto_ssh.Password("anypassword"),
		},
		HostKeyCallback: gocrypto_ssh.InsecureIgnoreHostKey(),
	}

	conn := dialSSHWithRetry(t, server.Addr, clientConfig)
	defer conn.Close()

	session, err := conn.NewSession()
	require.NoError(t, err)

	stdin, err := session.StdinPipe()
	require.NoError(t, err)
	stdout, err := session.StdoutPipe()
	require.NoError(t, err)

	err = session.Shell()
	require.NoError(t, err)

	_, err = stdin.Write([]byte("ping\n"))
	require.NoError(t, err)

	done := make(chan struct{})
	var output string
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if err != nil {
				break
			}
			output += string(buf[:n])
			if strings.Contains(output, "terminal-plugin-output") {
				break
			}
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for plugin output")
	}
	assert.Contains(t, output, "terminal-plugin-output")

	_, err = stdin.Write([]byte("exit\n"))
	require.NoError(t, err)

	session.Wait()

	time.Sleep(100 * time.Millisecond)

	foundInteraction := false
	foundEnd := false
	for _, e := range mt.events {
		if e.Msg == "SSH Terminal Session Interaction" {
			foundInteraction = true
			assert.Equal(t, "ping", e.Command)
			assert.Equal(t, "terminal-plugin-output", e.CommandOutput)
			assert.Equal(t, "ping-cmd", e.Handler)
		}
		if e.Msg == "End SSH Session" {
			foundEnd = true
		}
	}
	assert.True(t, foundInteraction, "should have terminal interaction event")
	assert.True(t, foundEnd, "should have end session event")
	assert.NoError(t, strategy.StopAll())
}

func TestSSHStrategy_Init_PasswordHandlerRejectsInvalidPassword(t *testing.T) {
	port := freePort(t)

	strategy := &SSHStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:                fmt.Sprintf("127.0.0.1:%d", port),
		Description:            "test SSH",
		DeadlineTimeoutSeconds: 2,
		PasswordRegex:          "^correct$",
		ServerVersion:          "SSH-2.0-test",
	}

	err := strategy.Init(servConf, mt)
	require.NoError(t, err)

	server := strategy.servers[servConf.Address]
	require.NotNil(t, server)

	waitForServer(t, server.Addr)

	clientConfig := &gocrypto_ssh.ClientConfig{
		User: "testuser",
		Auth: []gocrypto_ssh.AuthMethod{
			gocrypto_ssh.Password("wrong"),
		},
		HostKeyCallback: gocrypto_ssh.InsecureIgnoreHostKey(),
	}

	_, err = gocrypto_ssh.Dial("tcp", server.Addr, clientConfig)
	require.Error(t, err)

	time.Sleep(100 * time.Millisecond)

	foundLogin := false
	for _, e := range mt.events {
		if e.Msg == "New SSH Login Attempt" {
			foundLogin = true
			assert.Equal(t, "testuser", e.User)
			assert.Equal(t, "wrong", e.Password)
			break
		}
	}
	assert.True(t, foundLogin, "PasswordHandler should have been invoked")
	assert.NoError(t, strategy.StopAll())
}

func TestSSHStrategy_StopAll_ClearsCleanerOnce(t *testing.T) {
	strategy := &SSHStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:                "127.0.0.1:0",
		Description:            "test SSH",
		DeadlineTimeoutSeconds: 2,
		PasswordRegex:          ".*",
	}

	assert.NoError(t, strategy.Init(servConf, mt))
	assert.NoError(t, strategy.StopAll())
	assert.NoError(t, strategy.Init(servConf, mt))
	assert.NotNil(t, strategy.Sessions)
	require.NoError(t, strategy.StopAll())
}

func TestHandleSession_RawCommandWithUnknownPlugin(t *testing.T) {
	mt := &mockTracer{}
	sessions := historystore.NewHistoryStore()
	rex := regexp.MustCompile("^ls$")
	servConf := parser.BeelzebubServiceConfiguration{
		Description: "test",
		ServerName:  "test-server",
		Commands: []parser.Command{
			{Regex: rex, Plugin: "non-existent-plugin", Name: "ls-cmd"},
		},
	}
	sess := &mockSession{rawCmd: "ls", user: "root"}

	handleSession(sess, servConf, mt, sessions)

	// When plugin is unknown, commandOutput stays as command.Handler (empty)
	// session should complete normally with the Empty String message
	foundRawCommand := false
	for _, e := range mt.events {
		if e.Msg == "SSH Raw Command" {
			foundRawCommand = true
			break
		}
	}
	assert.True(t, foundRawCommand, "should have raw command event despite unknown plugin")
}

func TestSSHStrategy_Stop_ShutdownError(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	failL := &failOnCloseListener{Listener: l, closeErr: errors.New("stop shutdown error")}

	server := &ssh.Server{
		Handler: ssh.Handler(func(s ssh.Session) {}),
	}
	go func() {
		server.Serve(failL)
	}()
	time.Sleep(50 * time.Millisecond)

	strategy := &SSHStrategy{}
	strategy.servers = map[string]*ssh.Server{"test:0": server}

	servConf := parser.BeelzebubServiceConfiguration{
		Address: "test:0",
	}
	err = strategy.Stop(servConf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stop shutdown error")
	assert.Equal(t, 1, len(strategy.servers))
}
