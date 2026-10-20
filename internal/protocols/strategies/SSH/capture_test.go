package SSH

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
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
	"github.com/beelzebub-labs/beelzebub/v3/internal/testutil/durabletest"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
	"github.com/gliderlabs/ssh"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

type recordingSSHTracer struct {
	mu     sync.Mutex
	events []tracer.Event
}

func (r *recordingSSHTracer) TraceEvent(event tracer.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}
func (r *recordingSSHTracer) snapshot() []tracer.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]tracer.Event(nil), r.events...)
}

func captureSSHConfig() parser.BeelzebubServiceConfiguration {
	return parser.BeelzebubServiceConfiguration{Address: "127.0.0.1:0", Protocol: "ssh", Description: "synthetic SSH", ServerName: "synthetic",
		PasswordRegex: "^synthetic-password$", DeadlineTimeoutSeconds: 5,
		Commands: []parser.Command{{Regex: regexp.MustCompile("^(help|whoami)$"), Handler: "synthetic-output", Name: "synthetic-command"}}}
}

func startCaptureSSHServer(t *testing.T, capture tracer.Tracer, configs ...parser.BeelzebubServiceConfiguration) *gossh.Client {
	t.Helper()
	conf := captureSSHConfig()
	if len(configs) > 0 {
		conf = configs[0]
	}
	strategy := &SSHStrategy{Sessions: historystore.NewHistoryStore()}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := gossh.NewSignerFromKey(key)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	done := make(chan error, 1)
	ready := make(chan *ssh.Server, 1)

	require.NoError(t, strategy.initServer(conf, capture, func(srv *ssh.Server) error {
		srv.AddHostKey(signer)
		ready <- srv
		err := srv.Serve(listener)
		done <- err
		return err
	}))

	var srv *ssh.Server
	select {
	case srv = <-ready:
	case <-time.After(time.Second):
		t.Fatal("SSH server was not configured")
	}

	t.Cleanup(func() {
		srv.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("SSH listener did not stop")
		}
		strategy.Sessions.Close()
	})

	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	transport, channels, requests, err := gossh.NewClientConn(conn, listener.Addr().String(), &gossh.ClientConfig{
		User: "synthetic-user", Auth: []gossh.AuthMethod{gossh.Password("synthetic-password")}, HostKeyCallback: gossh.InsecureIgnoreHostKey()})

	require.NoError(t, err)
	client := gossh.NewClient(transport, channels, requests)
	t.Cleanup(func() { client.Close() })
	return client
}

func TestSSHAuthenticationAndSessionsCaptureWithAndWithoutSpool(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, interactive := range []bool{false, true} {
			name := "legacy"
			if durable {
				name = "durable"
			}

			if interactive {
				name += "-terminal"
			} else {
				name += "-exec"
			}

			t.Run(name, func(t *testing.T) {
				recorded := &recordingSSHTracer{}
				var capture tracer.Tracer = recorded
				var replay *durabletest.Capture
				if durable {
					replay = durabletest.New(t)
					capture = replay.Tracer
				}
				client := startCaptureSSHServer(t, capture)
				session, err := client.NewSession()
				require.NoError(t, err)
				t.Cleanup(func() { session.Close() })

				if interactive {
					session.Stdin = strings.NewReader("help\rexit\r")
					session.Stdout, session.Stderr = io.Discard, io.Discard
					require.NoError(t, session.Shell())
					require.NoError(t, session.Wait())
				} else {
					output, err := session.CombinedOutput("whoami")
					require.NoError(t, err)
					require.Equal(t, "synthetic-output\n", string(output))
				}

				events := recorded.snapshot()
				if durable {
					events = replay.Events()
					replay.VerifyReplay()
				}
				want := []string{"Stateless", "Start"}
				if interactive {
					want = append(want, "Interaction", "End")
				}

				require.Len(t, events, len(want))
				for i, event := range events {
					require.Equal(t, want[i], event.Status)
					require.Equal(t, "SSH", event.Protocol)
				}

				require.Equal(t, "synthetic-user", events[0].User)
				require.Equal(t, "synthetic-password", events[0].Password)
				require.Equal(t, "127.0.0.1", events[0].SourceIp)
				if interactive {
					require.Equal(t, events[1].ID, events[2].ID)
					require.Equal(t, events[1].ID, events[3].ID)
					require.Equal(t, "help", events[2].Command)
				} else {
					require.Equal(t, "whoami", events[1].Command)
				}
			})
		}
	}
}

type canceledSSHContext struct {
	context.Context
	sync.Mutex
}

func (*canceledSSHContext) User() string          { return "synthetic-user" }
func (*canceledSSHContext) SessionID() string     { return "synthetic-session" }
func (*canceledSSHContext) ClientVersion() string { return "SSH-2.0-synthetic" }
func (*canceledSSHContext) ServerVersion() string { return "SSH-2.0-synthetic" }
func (*canceledSSHContext) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 1234}
}
func (c *canceledSSHContext) LocalAddr() net.Addr { return c.RemoteAddr() }
func (*canceledSSHContext) Permissions() *ssh.Permissions {
	return &ssh.Permissions{Permissions: &gossh.Permissions{}}
}
func (c *canceledSSHContext) SetValue(key, value any) {
	c.Context = context.WithValue(c.Context, key, value)
}

type expiredSSHSession struct {
	ssh.Session
	ctx *canceledSSHContext
}

func (s expiredSSHSession) Context() ssh.Context         { return s.ctx }
func (s expiredSSHSession) RemoteAddr() net.Addr         { return s.ctx.RemoteAddr() }
func (s expiredSSHSession) User() string                 { return s.ctx.User() }
func (expiredSSHSession) RawCommand() string             { return "" }
func (expiredSSHSession) Environ() []string              { return nil }
func (expiredSSHSession) Read([]byte) (int, error)       { return 0, io.EOF }
func (expiredSSHSession) Write(body []byte) (int, error) { return len(body), nil }

func TestSSHExpiredProducerContextStillCapturesSessionEnd(t *testing.T) {
	type contextKey struct{}
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "synthetic-value"))
	cancel()
	ctx := &canceledSSHContext{Context: parent}
	strategy := &SSHStrategy{Sessions: historystore.NewHistoryStore()}
	t.Cleanup(strategy.Sessions.Close)
	var events []tracer.Event
	capture := tracer.NewDurableTracer(func(ctx context.Context, event tracer.Event) error {
		if event.Status == tracer.End.String() {
			require.NoError(t, ctx.Err(), "end capture must outlive the connection context")
			require.Equal(t, "synthetic-value", ctx.Value(contextKey{}))
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			require.Positive(t, time.Until(deadline))
			require.LessOrEqual(t, time.Until(deadline), 5*time.Second)
		}

		events = append(events, event)
		return nil
	})

	ready := make(chan *ssh.Server, 1)
	require.NoError(t, strategy.initServer(captureSSHConfig(), capture, func(srv *ssh.Server) error {
		ready <- srv
		return nil
	}))

	var srv *ssh.Server
	select {
	case srv = <-ready:
	case <-time.After(time.Second):
		t.Fatal("SSH server was not configured")
	}

	t.Cleanup(func() { srv.Close() })
	require.True(t, srv.PasswordHandler(ctx, "synthetic-password"))
	// Already built authentication/start events still get one admission attempt.
	require.Len(t, events, 1)
	require.Equal(t, "Stateless", events[0].Status)
	srv.Handler(expiredSSHSession{ctx: ctx})
	require.Len(t, events, 3)
	require.Equal(t, "Start", events[1].Status)
	require.Equal(t, "End", events[2].Status)
}

func TestSSHBlockedAuthenticationCaptureHonorsCancellationAndTimeout(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			spool, err := plugins.NewDurableEvents("http://synthetic.invalid", "synthetic-token", t.TempDir(), 0, 1)
			require.NoError(t, err)
			t.Cleanup(spool.Close)
			require.NoError(t, spool.Enqueue(context.Background(), tracer.Event{ID: "retained-synthetic"}))
			entered, released := make(chan struct{}), make(chan error, 1)
			capture := tracer.NewDurableTracer(func(ctx context.Context, event tracer.Event) error {
				close(entered)
				err := spool.Enqueue(ctx, event)
				released <- err
				return err
			})
			conf := captureSSHConfig()
			conf.DeadlineTimeoutSeconds = 1
			strategy := &SSHStrategy{Sessions: historystore.NewHistoryStore()}
			t.Cleanup(strategy.Sessions.Close)
			ready := make(chan *ssh.Server, 1)
			require.NoError(t, strategy.initServer(conf, capture, func(srv *ssh.Server) error { ready <- srv; return nil }))
			var srv *ssh.Server
			select {
			case srv = <-ready:
			case <-time.After(time.Second):
				t.Fatal("SSH server was not configured")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			returned := make(chan bool, 1)
			go func() { returned <- srv.PasswordHandler(&canceledSSHContext{Context: ctx}, "synthetic-password") }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("SSH authentication did not enter capture")
			}
			wantErr := context.DeadlineExceeded
			if mode == "cancel" {
				cancel()
				wantErr = context.Canceled
			}
			if mode == "shutdown" {
				spool.Close()
				wantErr = plugins.ErrEventSpoolClosed
			}
			select {
			case err := <-released:
				require.ErrorIs(t, err, wantErr)
			case <-time.After(2 * time.Second):
				spool.Close()
				<-returned
				t.Fatal("SSH authentication capture ignored its configured timeout")
			}
			require.True(t, <-returned)
		})
	}
}

type captureCommandPlugin struct {
	name  string
	fail  bool
	calls chan plugin.CommandRequest
}

func (p *captureCommandPlugin) Metadata() plugin.Metadata { return plugin.Metadata{Name: p.name} }
func (p *captureCommandPlugin) Execute(_ context.Context, request plugin.CommandRequest) (string, error) {
	request.History = append([]plugin.Message(nil), request.History...)
	p.calls <- request
	if p.fail {
		return "", errors.New("synthetic command failure")
	}
	return "synthetic-plugin-output", nil
}

func TestSSHPluginResultsAndHistorySurviveReplay(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		for _, mode := range []string{"success", "failure", "missing"} {
			name := "exec/" + mode
			if interactive {
				name = "terminal/" + mode
			}
			t.Run(name, func(t *testing.T) {
				replay := durabletest.New(t)
				p := &captureCommandPlugin{name: "synthetic-ssh-" + uuid.NewString(), fail: mode == "failure", calls: make(chan plugin.CommandRequest, 3)}
				if mode != "missing" {
					plugin.Register(p)
				}
				conf := captureSSHConfig()
				conf.Commands[0].Plugin = p.name
				client := startCaptureSSHServer(t, replay.Tracer, conf)
				wantOutput := "synthetic-plugin-output"
				if mode == "failure" {
					wantOutput = "command not found"
				}
				if mode == "missing" {
					wantOutput = "synthetic-output"
				}
				exec := func() {
					session, err := client.NewSession()
					require.NoError(t, err)
					defer session.Close()
					output, err := session.CombinedOutput("whoami")
					require.NoError(t, err)
					require.Equal(t, wantOutput+"\n", string(output))
				}
				exec()
				wantCalls := 2
				if interactive {
					session, err := client.NewSession()
					require.NoError(t, err)
					defer session.Close()
					session.Stdin = strings.NewReader("help\rhelp\rexit\r")
					session.Stdout, session.Stderr = io.Discard, io.Discard
					require.NoError(t, session.Shell())
					require.NoError(t, session.Wait())
					wantCalls = 3
				} else {
					exec()
				}
				if mode == "missing" {
					require.Empty(t, p.calls)
				} else {
					for i := range wantCalls {
						select {
						case call := <-p.calls:
							require.Equal(t, "ssh", call.Protocol)
							require.Equal(t, "127.0.0.1", call.ClientIP)
							require.Len(t, call.History, 2*i)
						case <-time.After(time.Second):
							t.Fatal("SSH plugin call was not observed")
						}
					}
				}
				events := replay.Events()
				commands := 0
				for _, event := range events {
					if event.Command != "" {
						commands++
						require.Equal(t, wantOutput, event.CommandOutput)
					}
				}
				require.Equal(t, wantCalls, commands)
				replay.VerifyReplay()
			})
		}
	}
}
