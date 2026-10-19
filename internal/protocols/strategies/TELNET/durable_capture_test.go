package TELNET

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	"github.com/beelzebub-labs/beelzebub/v3/internal/testutil/durabletest"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func captureTelnetConfig() parser.BeelzebubServiceConfiguration {
	return parser.BeelzebubServiceConfiguration{Protocol: "telnet", ServerName: "synthetic", PasswordRegex: "^synthetic-password$", DeadlineTimeoutSeconds: 5,
		Commands: []parser.Command{{Regex: regexp.MustCompile("^help$"), Handler: "synthetic-output"}}}
}

func telnetReadUntil(t *testing.T, reader *bufio.Reader, suffix string) string {
	t.Helper()
	var body strings.Builder
	for !strings.HasSuffix(body.String(), suffix) {
		b, err := reader.ReadByte()
		require.NoError(t, err)
		body.WriteByte(b)
	}

	return body.String()
}

func startTelnetCapture(t *testing.T, conf parser.BeelzebubServiceConfiguration, capture tracer.Tracer, strategies ...*TelnetStrategy) (net.Conn, net.Conn, *bufio.Reader, <-chan struct{}) {
	t.Helper()
	peer, conn := net.Pipe()
	t.Cleanup(func() { peer.Close(); conn.Close() })
	require.NoError(t, peer.SetDeadline(time.Now().Add(5*time.Second)))
	var strategy *TelnetStrategy

	if len(strategies) > 0 {
		strategy = strategies[0]
	} else {
		strategy = newTelnetStrategy()
	}

	t.Cleanup(strategy.Sessions.Close)
	done := make(chan struct{})

	go func() { defer close(done); handleTelnetConnection(conn, conf, capture, strategy) }()
	reader := bufio.NewReader(peer)
	telnetReadUntil(t, reader, "login: ")
	_, err := io.WriteString(peer, "synthetic-user\n")
	require.NoError(t, err)
	telnetReadUntil(t, reader, "Password: ")
	return peer, conn, reader, done
}

func waitTelnetCapture(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Telnet capture did not return")
	}
}

func TestTelnetCaptureReplayWithAndWithoutSpool(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, command := range []string{"help", "unknown", "invalid-password", "no-timeout"} {
			name := "legacy-" + command
			if durable {
				name = "durable-" + command
			}

			t.Run(name, func(t *testing.T) {
				recorded := &mockTracer{}
				var capture tracer.Tracer = recorded
				var replay *durabletest.Capture
				if durable {
					replay = durabletest.New(t)
					capture = replay.Tracer
				}

				conf := captureTelnetConfig()
				if command == "no-timeout" {
					conf.DeadlineTimeoutSeconds = 0
				}

				peer, _, reader, done := startTelnetCapture(t, conf, capture)
				password := "synthetic-password"
				if command == "invalid-password" {
					password = "synthetic-invalid"
				}

				_, err := io.WriteString(peer, password+"\n")
				require.NoError(t, err)
				if command != "invalid-password" {
					telnetReadUntil(t, reader, "synthetic-user@synthetic:~$ ")
					_, err := io.WriteString(peer, command+"\n")
					require.NoError(t, err)
					output := telnetReadUntil(t, reader, "synthetic-user@synthetic:~$ ")

					if command == "help" {
						require.Contains(t, output, "synthetic-output")
					} else {
						require.Contains(t, output, "command not found")
					}

					_, err = io.WriteString(peer, "exit\n")
					require.NoError(t, err)
				}

				_, err = io.Copy(io.Discard, reader)
				require.NoError(t, err)
				waitTelnetCapture(t, done)
				events := recorded.events

				if durable {
					events = replay.Events()
					replay.VerifyReplay()
				}

				want := []string{"Stateless"}
				if command != "invalid-password" {
					want = append(want, "Start", "Interaction", "End")
				}

				require.Len(t, events, len(want))
				for i, event := range events {
					require.Equal(t, "TELNET", event.Protocol)
					require.Equal(t, want[i], event.Status)
				}

				require.Equal(t, "synthetic-user", events[0].User)
				require.Equal(t, password, events[0].Password)
				if command != "invalid-password" {
					require.Equal(t, command, events[2].Command)
					require.Equal(t, events[1].ID, events[2].ID)
					require.Equal(t, events[1].ID, events[3].ID)
				}
			})
		}
	}
}

func TestTelnetFullSpoolCaptureHonorsConnectionTimeout(t *testing.T) {
	directory := t.TempDir()
	spool, err := plugins.NewDurableEvents("http://synthetic.invalid", "synthetic-token", directory, 0, 1)
	require.NoError(t, err)
	t.Cleanup(spool.Close)
	require.NoError(t, spool.Enqueue(context.Background(), tracer.Event{ID: "retained-synthetic"}))
	released := make(chan error, 1)
	capture := tracer.NewDurableTracer(func(ctx context.Context, event tracer.Event) error {
		err := spool.Enqueue(ctx, event)
		released <- err
		return err
	})

	conf := captureTelnetConfig()
	conf.DeadlineTimeoutSeconds = 1
	peer, _, reader, done := startTelnetCapture(t, conf, capture)
	_, err = io.WriteString(peer, "synthetic-invalid\n")
	require.NoError(t, err)

	// The expired connection closes rather than sending another login response.
	_, err = io.Copy(io.Discard, reader)
	require.NoError(t, err)
	waitTelnetCapture(t, done)

	select {
	case err := <-released:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		spool.Close()
		t.Fatal("blocked Telnet capture ignored connection timeout")
	}

	paths, err := filepath.Glob(filepath.Join(directory, "*.json"))
	require.NoError(t, err)
	require.Len(t, paths, 1)
}

func TestTelnetExpiredConnectionStillPersistsSessionEnd(t *testing.T) {
	replay := durabletest.New(t)
	type endContext struct {
		err      error
		deadline time.Time
		bounded  bool
	}

	observed := make(chan endContext, 1)
	capture := tracer.NewDurableTracer(func(ctx context.Context, event tracer.Event) error {
		if event.Status == "End" {
			deadline, bounded := ctx.Deadline()
			observed <- endContext{ctx.Err(), deadline, bounded}
		}

		return replay.Spool.Enqueue(ctx, event)
	})

	conf := captureTelnetConfig()
	conf.DeadlineTimeoutSeconds = 1
	peer, _, reader, done := startTelnetCapture(t, conf, capture)
	_, err := io.WriteString(peer, "synthetic-password\n")
	require.NoError(t, err)
	telnetReadUntil(t, reader, "synthetic-user@synthetic:~$ ")
	waitTelnetCapture(t, done)
	result := <-observed
	require.NoError(t, result.err)
	require.True(t, result.bounded)
	require.Positive(t, time.Until(result.deadline))
	require.LessOrEqual(t, time.Until(result.deadline), 5*time.Second)
	events := replay.Events()
	require.Len(t, events, 3)
	require.Equal(t, "End", events[2].Status)
	replay.VerifyReplay()
}

type failedDeadlineConn struct{ net.Conn }

func (failedDeadlineConn) SetDeadline(time.Time) error {
	return errors.New("synthetic deadline failure")
}

func TestTelnetDeadlineFailureClosesBeforeCapture(t *testing.T) {
	peer, conn := net.Pipe()
	t.Cleanup(func() { peer.Close(); conn.Close() })
	require.NoError(t, peer.SetDeadline(time.Now().Add(time.Second)))
	strategy := newTelnetStrategy()
	t.Cleanup(strategy.Sessions.Close)
	capture := &mockTracer{}
	done := make(chan struct{})

	go func() {
		defer close(done)
		handleTelnetConnection(failedDeadlineConn{conn}, captureTelnetConfig(), capture, strategy)
	}()

	body, err := io.ReadAll(peer)
	require.NoError(t, err)
	waitTelnetCapture(t, done)
	require.Empty(t, body)
	require.Empty(t, capture.events)
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

func TestTelnetPluginResultsAndHistorySurviveReplay(t *testing.T) {
	for _, mode := range []string{"success", "failure", "missing"} {
		t.Run(mode, func(t *testing.T) {
			replay := durabletest.New(t)
			var captured []tracer.Event

			capture := tracer.NewDurableTracer(func(ctx context.Context, event tracer.Event) error {
				if err := replay.Spool.Enqueue(ctx, event); err != nil {
					return err
				}

				captured = append(captured, event)
				return nil
			})

			p := &captureCommandPlugin{name: "synthetic-telnet-" + uuid.NewString(), fail: mode == "failure", calls: make(chan plugin.CommandRequest, 2)}
			if mode != "missing" {
				plugin.Register(p)
			}

			conf := captureTelnetConfig()
			conf.Commands[0].Plugin = p.name
			conf.Commands[0].Handler = "synthetic-fallback"
			conf.Commands[0].Name = "synthetic-plugin-command"
			strategy := newTelnetStrategy()
			strategy.Sessions.Append("TELNETsynthetic-user", plugins.Message{Role: "user", Content: "prior command"}, plugins.Message{Role: "assistant", Content: "prior output"})
			peer, _, reader, done := startTelnetCapture(t, conf, capture, strategy)
			_, err := io.WriteString(peer, "synthetic-password\n")
			require.NoError(t, err)
			telnetReadUntil(t, reader, "synthetic-user@synthetic:~$ ")
			wantOutput := "synthetic-plugin-output"
			if mode == "failure" {
				wantOutput = "command not found"
			}

			if mode == "missing" {
				wantOutput = "synthetic-fallback"
			}

			for range 2 {
				_, err = io.WriteString(peer, "help\n")
				require.NoError(t, err)
				output := telnetReadUntil(t, reader, "synthetic-user@synthetic:~$ ")
				require.Contains(t, output, wantOutput)
			}

			_, err = io.WriteString(peer, "exit\n")
			require.NoError(t, err)
			_, err = io.Copy(io.Discard, reader)
			require.NoError(t, err)
			waitTelnetCapture(t, done)

			if mode == "missing" {
				require.Empty(t, p.calls)
			} else {
				for i := range 2 {
					select {
					case call := <-p.calls:
						require.Equal(t, "telnet", call.Protocol)
						require.Len(t, call.History, 2+2*i)
					case <-time.After(time.Second):
						t.Fatal("Telnet plugin call was not observed")
					}
				}
			}

			events := replay.Events()
			require.Len(t, events, 5)
			require.Len(t, captured, 5)

			for _, event := range captured[2:4] {
				require.Equal(t, "synthetic-plugin-command", event.Handler)
			}

			for _, event := range events[2:4] {
				require.Equal(t, "help", event.Command)
				require.Equal(t, wantOutput, event.CommandOutput)
			}

			replay.VerifyReplay()
		})
	}
}

func TestTelnetReadLineSubnegotiationAndTruncation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wire    []byte
		want    string
		wantErr bool
	}{
		{"subnegotiation", []byte{IAC, SB, 24, 'x', IAC, SE, 'h', 'i', '\n'}, "hi", false},
		{"truncated-command", []byte{IAC}, "", true},
		{"truncated-subnegotiation", []byte{IAC, SB, 24}, "", true},
		{"truncated-terminator", []byte{IAC, SB, 24, IAC}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer, conn := net.Pipe()
			t.Cleanup(func() { peer.Close(); conn.Close() })
			require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
			done := make(chan error, 1)

			go func() { _, err := peer.Write(tc.wire); peer.Close(); done <- err }()
			line, err := readLine(conn)

			if tc.wantErr {
				require.ErrorIs(t, err, io.EOF)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, line)
			}

			require.NoError(t, <-done)
		})
	}
}
