package TCP

import (
	"context"
	"net"
	"os"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/testutil/durabletest"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/stretchr/testify/require"
)

func TestTCPTimeoutCapturePreservesAllAttemptsAndSessionEnds(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, interactive := range []bool{false, true} {
			name := "legacy-stateless"
			if interactive {
				name = "legacy-session"
			}

			if durable {
				name = "durable-" + name
			}

			t.Run(name, func(t *testing.T) {
				recorded := &mockTracer{}
				var capture tracer.Tracer = recorded
				var replay *durabletest.Capture
				if durable {
					replay = durabletest.New(t)
					capture = replay.Tracer
				}

				config := parser.BeelzebubServiceConfiguration{Protocol: "tcp", DeadlineTimeoutSeconds: 1}
				if interactive {
					config.Commands = []parser.Command{{Regex: regexp.MustCompile("never-match"), Handler: ""}}
				}

				const attempts = 20
				var wg sync.WaitGroup
				for range attempts {
					peer, conn := net.Pipe()
					t.Cleanup(func() { peer.Close(); conn.Close() })
					wg.Go(func() { handleTCPConnection(conn, config, capture, newStrategyWithSessions()); peer.Close() })
				}

				done := make(chan struct{})
				go func() { wg.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(4 * time.Second):
					t.Fatal("idle handlers did not finish after deadline")
				}

				events := recorded.snapshot()
				if durable {
					events = replay.Events()
					replay.VerifyReplay()
				}

				counts := map[string]int{}
				sessions := map[string]map[string]int{}
				for _, event := range events {
					counts[event.Status]++
					if sessions[event.ID] == nil {
						sessions[event.ID] = map[string]int{}
					}
					sessions[event.ID][event.Status]++
				}

				require.Len(t, sessions, attempts)
				if interactive {
					require.Equal(t, map[string]int{"Start": attempts, "End": attempts}, counts)
					for _, statuses := range sessions {
						require.Equal(t, map[string]int{"Start": 1, "End": 1}, statuses)
					}
				} else {
					require.Equal(t, map[string]int{"Stateless": attempts}, counts)
				}
			})
		}
	}
}

func TestTCPTerminalCaptureGetsLiveBoundedContext(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "stateless", true: "session"}[interactive], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var captured []tracer.Event

			capture := tracer.NewDurableTracer(func(eventCtx context.Context, event tracer.Event) error {
				require.NoError(t, eventCtx.Err(), "terminal capture inherited expired producer context")
				if event.Status == tracer.End.String() || event.Status == tracer.Stateless.String() {
					require.ErrorIs(t, ctx.Err(), context.Canceled)
					deadline, ok := eventCtx.Deadline()
					require.True(t, ok)
					require.Greater(t, time.Until(deadline), time.Duration(0))
					require.LessOrEqual(t, time.Until(deadline), 5*time.Second)
				}

				captured = append(captured, event)
				return nil
			})

			peer, conn := net.Pipe()
			defer peer.Close()
			done := make(chan struct{})
			config := parser.BeelzebubServiceConfiguration{Protocol: "tcp"}
			if interactive {
				config.Commands = []parser.Command{{Regex: regexp.MustCompile("never"), Handler: ""}}
			}

			go func() {
				defer close(done)
				handleTCPConnectionWithState(conn, config, capture, newStrategyWithSessions(), nil, ctx)
			}()

			cancel()
			peer.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("terminal capture blocked")
			}

			// Start capture can legitimately race cancellation; terminal capture cannot.
			found := false
			for _, event := range captured {
				if event.Status == tracer.End.String() || event.Status == tracer.Stateless.String() {
					found = true
				}
			}

			require.True(t, found)
		})
	}
}

type timeoutWithDataConn struct {
	net.Conn
	cancel context.CancelFunc
	done   bool
}

func (c *timeoutWithDataConn) Read(body []byte) (int, error) {
	c.cancel()
	if c.done {
		return 0, os.ErrDeadlineExceeded
	}

	c.done = true
	return copy(body, "payload"), os.ErrDeadlineExceeded
}

func TestTCPTerminalBufferedInteractionsSurviveExpiredContext(t *testing.T) {
	for _, matched := range []bool{false, true} {
		t.Run(map[bool]string{false: "unmatched", true: "matched"}[matched], func(t *testing.T) {
			peer, conn := net.Pipe()
			defer peer.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			capture := &mockTracer{}
			pattern := "never"
			if matched {
				pattern = "^payload$"
			}

			config := parser.BeelzebubServiceConfiguration{Protocol: "tcp", Commands: []parser.Command{{Regex: regexp.MustCompile(pattern), Handler: ""}}}
			handleTCPConnectionWithState(&timeoutWithDataConn{Conn: conn, cancel: cancel}, config, capture, newStrategyWithSessions(), nil, ctx)
			events := capture.snapshot()
			require.Len(t, events, 3)
			require.Equal(t, "Start", events[0].Status)
			require.Equal(t, "Interaction", events[1].Status)
			require.Equal(t, "payload", events[1].Command)
			require.Equal(t, "End", events[2].Status)
		})
	}
}
