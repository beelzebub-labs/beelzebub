// Package durabletest provides a shared restart/replay contract for protocol tests.
package durabletest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Capture uses real synchronous disk persistence and a disposable Platform endpoint.
// Delivery starts only after the protocol has finished producing its events.
type Capture struct {
	Tracer    tracer.Tracer
	Spool     *plugins.DurableEvents
	Directory string
	t         *testing.T
	endpoint  *httptest.Server
	recovered atomic.Bool
	attempts  chan attempt
}

type attempt struct {
	body   []byte
	status int
}

func New(t *testing.T) *Capture {
	t.Helper()
	c := &Capture{t: t, Directory: t.TempDir(), attempts: make(chan attempt, 128)}

	c.endpoint = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		var event tracer.Event
		if r.Method != http.MethodPost || r.URL.Path != "/events" || r.Header.Get("Authorization") != "synthetic-token" || err != nil || json.Unmarshal(body, &event) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		status := http.StatusServiceUnavailable
		if c.recovered.Load() {
			status = http.StatusOK
		}

		w.WriteHeader(status)
		if status == http.StatusOK {
			json.NewEncoder(w).Encode(map[string]string{"EventKey": event.EventKey})
		}

		c.attempts <- attempt{body: body, status: status}
	}))

	t.Cleanup(c.endpoint.Close)
	var err error
	c.Spool, err = plugins.NewDurableEvents(c.endpoint.URL, "synthetic-token", c.Directory, 0, 0)
	require.NoError(t, err)
	t.Cleanup(c.Spool.Close)
	c.Tracer = tracer.NewDurableTracer(c.Spool.Enqueue)
	return c
}

func (c *Capture) records() []json.RawMessage {
	c.t.Helper()
	paths, err := filepath.Glob(filepath.Join(c.Directory, "*.json"))
	require.NoError(c.t, err)
	var payloads []json.RawMessage

	for _, path := range paths {
		body, err := os.ReadFile(path)
		require.NoError(c.t, err)
		var record struct {
			Payload json.RawMessage `json:"payload"`
		}

		require.NoError(c.t, json.Unmarshal(body, &record))
		payloads = append(payloads, record.Payload)
	}

	return payloads
}

func (c *Capture) Events() []tracer.Event {
	c.t.Helper()
	var events []tracer.Event

	for _, payload := range c.records() {
		var event tracer.Event
		require.NoError(c.t, json.Unmarshal(payload, &event))
		key, err := uuid.Parse(event.EventKey)
		require.NoError(c.t, err)
		require.Equal(c.t, uuid.Version(7), key.Version())
		require.NotEmpty(c.t, event.DateTime)
		events = append(events, event)
	}

	return events
}

// VerifyReplay checks failed delivery, retained records, reopen, stable identity,
// identical payloads, capture order, and deletion only after successful acknowledgement.
// Process/container crash survival is separately exercised by integration_test/durable_events.py.
func (c *Capture) VerifyReplay() {
	c.t.Helper()
	expected := c.records()
	require.NotEmpty(c.t, expected)
	c.Spool.Start()

	select {
	case failed := <-c.attempts:
		require.Equal(c.t, http.StatusServiceUnavailable, failed.status)
		require.True(c.t, bytes.Equal(expected[0], failed.body), "failed POST changed the original event")
	case <-time.After(5 * time.Second):
		c.t.Fatal("protocol event did not reach the failing Platform endpoint")
	}

	c.Spool.Close()
	require.Equal(c.t, expected, c.records(), "unacknowledged protocol events must survive close")
	c.recovered.Store(true)
	reopened, err := plugins.NewDurableEvents(c.endpoint.URL, "synthetic-token", c.Directory, 0, 0)
	require.NoError(c.t, err)
	c.t.Cleanup(reopened.Close)
	reopened.Start()

	for _, payload := range expected {
		select {
		case delivered := <-c.attempts:
			require.Equal(c.t, http.StatusOK, delivered.status)
			require.True(c.t, bytes.Equal(payload, delivered.body), "replay changed event identity, payload, or order")
		case <-time.After(5 * time.Second):
			c.t.Fatal("protocol event did not replay after recovery")
		}
	}

	require.Eventually(c.t, func() bool {
		paths, err := filepath.Glob(filepath.Join(c.Directory, "*.json"))
		return err == nil && len(paths) == 0
	}, 5*time.Second, 10*time.Millisecond, "acknowledged events were not removed")
}
