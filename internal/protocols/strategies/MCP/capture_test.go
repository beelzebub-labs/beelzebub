package MCP

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	"github.com/beelzebub-labs/beelzebub/v3/internal/testutil/durabletest"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type recordingMCPTracer struct {
	mu     sync.Mutex
	events []tracer.Event
}

func (r *recordingMCPTracer) TraceEvent(event tracer.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recordingMCPTracer) snapshot() []tracer.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]tracer.Event(nil), r.events...)
}

func captureMCPConfig() parser.BeelzebubServiceConfiguration {
	return parser.BeelzebubServiceConfiguration{Protocol: "mcp", Description: "synthetic MCP",
		Tools: []parser.Tool{{Name: "query_logs", Handler: "synthetic-output", Params: []parser.Param{{Name: "filter", Description: "Synthetic filter"}}}}}
}

// Keep ownership of the listener and shutdown in tests while exercising production callbacks.
func configuredMCPHandler(t *testing.T, capture tracer.Tracer) *server.StreamableHTTPServer {
	t.Helper()
	ready := make(chan *server.StreamableHTTPServer, 1)
	strategy := &MCPStrategy{}
	require.NoError(t, strategy.initServer(captureMCPConfig(), capture, func(srv *server.StreamableHTTPServer) error {
		ready <- srv
		return nil
	}))

	select {
	case srv := <-ready:
		return srv
	case <-time.After(time.Second):
		t.Fatal("MCP server was not configured")
		return nil
	}
}

func mcpCaptureRequest(ctx context.Context, url, session, method string, versions ...string) (*http.Request, error) {
	version := mcp.LATEST_LEGACY_PROTOCOL_VERSION
	if len(versions) > 0 {
		version = versions[0]
	}
	params := map[string]any{"name": "query_logs", "arguments": map[string]any{"filter": "synthetic"}}
	if method == "initialize" {
		params = map[string]any{"protocolVersion": mcp.LATEST_LEGACY_PROTOCOL_VERSION, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "synthetic-client", "version": "1"}}
	}

	if version == mcp.LATEST_PROTOCOL_VERSION {
		params["_meta"] = map[string]any{
			mcp.MetaKeyProtocolVersion:    version,
			mcp.MetaKeyClientInfo:         map[string]any{"name": "synthetic-client", "version": "1"},
			mcp.MetaKeyClientCapabilities: map[string]any{},
		}
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/mcp", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", version)
	if version == mcp.LATEST_PROTOCOL_VERSION {
		req.Header.Set(mcp.HeaderMethod, method)
		req.Header.Set(mcp.HeaderName, "query_logs")
	}
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}

	if method == "tools/call" {
		req.Header.Set("X-Synthetic-Capture", "tool")
	}

	return req, nil
}

func initializeCaptureMCP(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := mcpCaptureRequest(ctx, srv.URL, "", "initialize")
	require.NoError(t, err)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	require.NotContains(t, string(body), `"error"`)
	session := resp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, session)
	return session
}

func TestMCPToolInvocationCapturesWithAndWithoutSpool(t *testing.T) {
	for _, version := range []string{mcp.LATEST_LEGACY_PROTOCOL_VERSION, mcp.LATEST_PROTOCOL_VERSION} {
		for _, durable := range []bool{false, true} {
			name := "legacy"
			if durable {
				name = "durable"
			}

			t.Run(version+"/"+name, func(t *testing.T) {
				recorded := &recordingMCPTracer{}
				var capture tracer.Tracer = recorded
				var replay *durabletest.Capture
				if durable {
					replay = durabletest.New(t)
					capture = replay.Tracer
				}
				handler := configuredMCPHandler(t, capture)
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					require.NoError(t, handler.Shutdown(ctx))
				})

				srv := httptest.NewServer(handler)
				t.Cleanup(srv.Close)
				session := ""
				if version == mcp.LATEST_LEGACY_PROTOCOL_VERSION {
					session = initializeCaptureMCP(t, srv)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				req, err := mcpCaptureRequest(ctx, srv.URL, session, "tools/call", version)
				require.NoError(t, err)
				resp, err := srv.Client().Do(req)
				require.NoError(t, err)
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
				require.Contains(t, string(body), "synthetic-output")
				events := recorded.snapshot()

				if durable {
					events = replay.Events()
					replay.VerifyReplay()
				}
				require.Len(t, events, 1)
				require.Equal(t, "MCP", events[0].Protocol)
				require.Equal(t, "Stateless", events[0].Status)
				require.Equal(t, "127.0.0.1", events[0].SourceIp)
				require.NotEmpty(t, events[0].SourcePort)
				require.Contains(t, events[0].Command, "query_logs|")
				require.Contains(t, events[0].Command, "synthetic")
				require.Equal(t, "synthetic-output", events[0].CommandOutput)
			})
		}
	}
}

func TestMCPDisconnectCancelsBlockedDurableCapture(t *testing.T) {
	for _, version := range []string{mcp.LATEST_LEGACY_PROTOCOL_VERSION, mcp.LATEST_PROTOCOL_VERSION} {
		t.Run(version, func(t *testing.T) {
			directory := t.TempDir()
			spool, err := plugins.NewDurableEvents("http://synthetic.invalid", "synthetic-token", directory, 0, 1)
			require.NoError(t, err)
			t.Cleanup(spool.Close)
			require.NoError(t, spool.Enqueue(context.Background(), tracer.Event{ID: "synthetic-existing"}))
			entered, released, finished := make(chan struct{}), make(chan error, 1), make(chan struct{})

			capture := tracer.NewDurableTracer(func(ctx context.Context, event tracer.Event) error {
				close(entered)
				err := spool.Enqueue(ctx, event)
				released <- err
				return err
			})

			handler := configuredMCPHandler(t, capture)
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				require.NoError(t, handler.Shutdown(ctx))
			})

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Synthetic-Capture") == "tool" {
					defer close(finished)
				}
				handler.ServeHTTP(w, r)
			}))

			t.Cleanup(srv.Close)
			session := ""
			if version == mcp.LATEST_LEGACY_PROTOCOL_VERSION {
				session = initializeCaptureMCP(t, srv)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, err := mcpCaptureRequest(ctx, srv.URL, session, "tools/call", version)
			require.NoError(t, err)
			clientDone := make(chan error, 1)

			go func() {
				resp, err := srv.Client().Do(req)
				if resp != nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}

				clientDone <- err
			}()

			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("tool invocation did not enter capture")
			}

			cancel()
			select {
			case err := <-released:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(time.Second):
				spool.Close()
				t.Fatal("MCP disconnect retained blocked capture")
			}

			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("MCP callback did not return")
			}

			require.ErrorIs(t, <-clientDone, context.Canceled)
			paths, err := filepath.Glob(filepath.Join(directory, "*.json"))
			require.NoError(t, err)
			require.Len(t, paths, 1, "the canceled tool event must not displace the previously durable record")
			body, err := os.ReadFile(paths[0])
			require.NoError(t, err)
			require.Contains(t, string(body), "synthetic-existing")
		})
	}
}

type startupFailureLog struct{ seen chan struct{} }

func (w startupFailureLog) Write(body []byte) (int, error) {
	if bytes.Contains(body, []byte("synthetic MCP bind failure")) {
		select {
		case w.seen <- struct{}{}:
		default:
		}
	}
	return len(body), nil
}

func TestMCPServerStartupFailureIsLogged(t *testing.T) {
	seen := make(chan struct{}, 1)
	previous := log.StandardLogger().Out
	log.SetOutput(startupFailureLog{seen})
	t.Cleanup(func() { log.SetOutput(previous) })
	ready := make(chan *server.StreamableHTTPServer, 1)
	strategy := &MCPStrategy{}
	require.NoError(t, strategy.initServer(captureMCPConfig(), &recordingMCPTracer{}, func(srv *server.StreamableHTTPServer) error {
		ready <- srv
		return errors.New("synthetic MCP bind failure")
	}))
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("MCP startup failure was not diagnosed")
	}
	srv := <-ready
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))
}
