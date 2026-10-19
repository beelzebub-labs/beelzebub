package HTTP

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	"github.com/beelzebub-labs/beelzebub/v3/internal/testutil/durabletest"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/stretchr/testify/require"
)

func fullCaptureSpool(t *testing.T) (*plugins.DurableEvents, string) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "events")
	spool, err := plugins.NewDurableEvents("http://127.0.0.1:1", "synthetic-token", directory, 0, 1)
	require.NoError(t, err)
	t.Cleanup(spool.Close)
	require.NoError(t, spool.Enqueue(context.Background(), tracer.Event{ID: "already-durable"}))
	return spool, directory
}

func captureConfiguration() parser.BeelzebubServiceConfiguration {
	return parser.BeelzebubServiceConfiguration{Protocol: "http", Commands: []parser.Command{{Regex: regexp.MustCompile(".*"), Handler: "synthetic", StatusCode: 200}}}
}

func TestDurableHandlerCancellationReleasesCapture(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		name := "full"
		if unavailable {
			name = "unavailable"
		}

		t.Run(name, func(t *testing.T) {
			spool, directory := fullCaptureSpool(t)
			if unavailable {
				require.NoError(t, os.Rename(directory, directory+"-retained"))
				require.NoError(t, os.WriteFile(directory, []byte("synthetic unavailable directory"), 0600))
			}

			entered := make(chan struct{})
			result := make(chan error, 1)
			capture := tracer.NewDurableTracer(func(ctx context.Context, event tracer.Event) error {
				close(entered)
				err := spool.Enqueue(ctx, event)
				result <- err
				return err
			})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := httptest.NewRequest(http.MethodPost, "http://synthetic.test/capture", strings.NewReader("synthetic body")).WithContext(ctx)
			done := make(chan struct{})

			go func() { newHTTPHandler(captureConfiguration(), capture)(httptest.NewRecorder(), request); close(done) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("handler never entered capture")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				spool.Close()
				t.Fatal("request cancellation retained handler until spool closed")
			}

			require.ErrorIs(t, <-result, context.Canceled)
			if unavailable {
				directory += "-retained"
			}

			files, err := filepath.Glob(filepath.Join(directory, "*.json"))
			require.NoError(t, err)
			require.Len(t, files, 1)
		})
	}
}

func TestDisconnectedHTTPClientsReleaseAllBlockedCaptures(t *testing.T) {
	spool, directory := fullCaptureSpool(t)
	const requests = 12
	entered := make(chan struct{}, requests)
	released := make(chan error, requests)
	finished := make(chan struct{}, requests)
	capture := tracer.NewDurableTracer(func(ctx context.Context, event tracer.Event) error {
		entered <- struct{}{}
		err := spool.Enqueue(ctx, event)
		released <- err
		return err
	})

	handler := newHTTPHandler(captureConfiguration(), capture)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer func() { finished <- struct{}{} }(); handler(w, r) }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var clients sync.WaitGroup
	for range requests {
		clients.Go(func() {
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader("synthetic body"))
			if err != nil {
				t.Error(err)
				return
			}

			response, err := server.Client().Do(request)
			if err == nil {
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
				t.Error("capture unexpectedly accepted request")
			} else if !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		})
	}

	for range requests {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			spool.Close()
			t.Fatal("clients never entered capture")
		}
	}

	cancel()
	clients.Wait()
	for range requests {
		select {
		case err := <-released:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(3 * time.Second):
			spool.Close()
			t.Fatal("disconnected client retained capture")
		}
		select {
		case <-finished:
		case <-time.After(time.Second):
			spool.Close()
			t.Fatal("handler did not exit")
		}
	}

	files, err := filepath.Glob(filepath.Join(directory, "*.json"))
	require.NoError(t, err)
	require.Len(t, files, 1)
}

func TestHTTPAndHTTPSCaptureReplayWithAndWithoutSpool(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, mode := range []string{"http", "https", "fallback", "method-rejected"} {
			name := "legacy-" + mode
			if durable {
				name = "durable-" + mode
			}
			t.Run(name, func(t *testing.T) {
				recorded := &mockTracer{}
				var capture tracer.Tracer = recorded
				var replay *durabletest.Capture
				if durable {
					replay = durabletest.New(t)
					capture = replay.Tracer
				}
				conf := captureConfiguration()
				wantStatus := http.StatusOK
				wantBody := "synthetic body"
				if mode == "fallback" {
					conf.FallbackCommand = conf.Commands[0]
					conf.Commands = nil
				}
				if mode == "method-rejected" {
					conf.Commands[0].Methods = []string{http.MethodGet}
					wantStatus = http.StatusMethodNotAllowed
					wantBody = ""
				}
				handler := newHTTPHandler(conf, capture)
				server := httptest.NewUnstartedServer(handler)
				if mode == "https" {
					server.StartTLS()
				} else {
					server.Start()
				}
				t.Cleanup(server.Close)
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/capture", strings.NewReader("synthetic body"))
				require.NoError(t, err)
				resp, err := server.Client().Do(req)
				require.NoError(t, err)
				_, err = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				require.NoError(t, err)
				require.Equal(t, wantStatus, resp.StatusCode)
				events := recorded.events
				if durable {
					events = replay.Events()
					replay.VerifyReplay()
				}
				require.Len(t, events, 1)
				require.Equal(t, "HTTP", events[0].Protocol)
				require.Equal(t, http.MethodPost, events[0].HTTPMethod)
				require.Equal(t, "/capture", events[0].RequestURI)
				require.Equal(t, wantBody, events[0].Body)
			})
		}
	}
}
