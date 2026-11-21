package TCP

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"regexp"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	"github.com/beelzebub-labs/beelzebub/v3/internal/testutil/durabletest"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
	"github.com/stretchr/testify/require"
)

func TestTCPCommandAndBinaryCaptureReplay(t *testing.T) {
	for _, mode := range []string{"matched", "unmatched", "binary"} {
		t.Run(mode, func(t *testing.T) {
			replay := durabletest.New(t)
			config := parser.BeelzebubServiceConfiguration{Protocol: "tcp", DeadlineTimeoutSeconds: 3,
				Commands: []parser.Command{{Regex: regexp.MustCompile("^synthetic-command$"), Handler: ""}}}

			payload := []byte("synthetic-command\n")
			if mode == "unmatched" {
				payload = []byte("unknown\n")
			}

			if mode == "binary" {
				config.Commands = nil
				payload = []byte{0xff, 0, 'a', 'b'}
			}

			peer, conn := net.Pipe()
			t.Cleanup(func() { peer.Close(); conn.Close() })
			strategy := newStrategyWithSessions()
			t.Cleanup(strategy.Sessions.Close)
			done := make(chan struct{})

			go func() { defer close(done); handleTCPConnection(conn, config, replay.Tracer, strategy) }()
			_, err := peer.Write(payload)
			require.NoError(t, err)
			peer.Close()

			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("TCP command capture did not return")
			}

			events := replay.Events()
			if mode == "binary" {
				require.Len(t, events, 1)
				require.Contains(t, events[0].CommandRaw, `\xff\x00`)
			} else {
				require.Len(t, events, 3)
				require.Equal(t, "Start", events[0].Status)
				require.Equal(t, "Interaction", events[1].Status)
				require.Equal(t, "End", events[2].Status)
				require.Equal(t, events[0].ID, events[1].ID)
				require.Equal(t, events[0].ID, events[2].ID)
			}

			replay.VerifyReplay()
		})
	}
}

func TestTCPBlockedCaptureCancellationAndShutdown(t *testing.T) {
	spool, err := plugins.NewDurableEvents("http://synthetic.invalid", "synthetic-token", t.TempDir(), 0, 1)
	require.NoError(t, err)
	t.Cleanup(spool.Close)
	require.NoError(t, spool.Enqueue(context.Background(), tracer.Event{ID: "retained-synthetic"}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, ended := make(chan struct{}), make(chan context.Context, 1)
	released := make(chan error, 2)

	capture := tracer.NewDurableTracer(func(eventCtx context.Context, event tracer.Event) error {
		if event.Status == "Start" {
			close(started)
		} else if event.Status == "End" {
			ended <- eventCtx
		}

		err := spool.Enqueue(eventCtx, event)
		released <- err
		return err
	})

	peer, conn := net.Pipe()
	t.Cleanup(func() { peer.Close(); conn.Close() })
	strategy := newStrategyWithSessions()
	t.Cleanup(strategy.Sessions.Close)
	done := make(chan struct{})

	go func() {
		defer close(done)
		handleTCPConnectionWithState(conn, parser.BeelzebubServiceConfiguration{Protocol: "tcp", Commands: []parser.Command{{Regex: regexp.MustCompile("never")}}}, capture, strategy, nil, ctx)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("TCP session did not enter capture")
	}

	cancel()
	peer.Close()
	select {
	case err := <-released:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		spool.Close()
		t.Fatal("TCP capture ignored producer cancellation")
	}

	select {
	case endCtx := <-ended:
		require.NoError(t, endCtx.Err())
		deadline, bounded := endCtx.Deadline()
		require.True(t, bounded)
		require.Positive(t, time.Until(deadline))
	case <-time.After(time.Second):
		spool.Close()
		t.Fatal("TCP session did not attempt teardown capture")
	}

	spool.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("TCP capture did not stop on spool shutdown")
	}

	require.ErrorIs(t, <-released, plugins.ErrEventSpoolClosed)
}

func TestTCPTLSUpgradeAndWireMetadataReplay(t *testing.T) {
	replay := durabletest.New(t)
	name := registerTestWire(t, &testWirePlugin{onWire: func(exchange *plugin.WireContext) error {
		exchange.Metadata = map[string]string{"synthetic": "preserved-wire-metadata"}
		return nil
	}})

	conf := parser.BeelzebubServiceConfiguration{Protocol: "tcp", ServerName: "synthetic.invalid", DeadlineTimeoutSeconds: 5, WirePlugins: []string{name},
		Commands: []parser.Command{
			{Regex: regexp.MustCompile("^starttls$"), Handler: "ready", TLSUpgrade: true},
			{Regex: regexp.MustCompile("^help$"), Handler: "encrypted-output", CloseAfter: true},
		}}

	peer, conn := net.Pipe()
	t.Cleanup(func() { peer.Close(); conn.Close() })
	require.NoError(t, peer.SetDeadline(time.Now().Add(5*time.Second)))
	strategy := newStrategyWithSessions()
	t.Cleanup(strategy.Sessions.Close)
	done := make(chan struct{})

	go func() { defer close(done); handleTCPConnection(conn, conf, replay.Tracer, strategy) }()
	_, err := io.WriteString(peer, "starttls\n")
	require.NoError(t, err)
	ready := make([]byte, len("ready"))
	_, err = io.ReadFull(peer, ready)
	require.NoError(t, err)
	require.Equal(t, "ready", string(ready))
	client := tls.Client(peer, &tls.Config{InsecureSkipVerify: true}) // Disposable self-signed test certificate.
	require.NoError(t, client.Handshake())
	_, err = io.WriteString(client, "help\n")
	require.NoError(t, err)
	output := make([]byte, len("encrypted-output"))
	_, err = io.ReadFull(client, output)
	require.NoError(t, err)
	require.Equal(t, "encrypted-output", string(output))
	_, err = io.Copy(io.Discard, client)
	require.NoError(t, err)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("TLS session did not finish")
	}

	events := replay.Events()
	require.Len(t, events, 4)
	require.Equal(t, "starttls", events[1].Command)
	require.Equal(t, "help", events[2].Command)
	require.Equal(t, "preserved-wire-metadata", events[1].Metadata["synthetic"])
	require.Equal(t, "preserved-wire-metadata", events[2].Metadata["synthetic"])
	replay.VerifyReplay()
}
