package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDurableLargeAcknowledgementsDrainFollowingEvents(t *testing.T) {
	for _, size := range []int{70000, 1024 * 1024} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			var mu sync.Mutex
			calls := map[string]int{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var event EventDTO
				if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				calls[event.EventKey]++
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				// Core echoes EventDTO. Put its identity after captured data so the
				// response must be read completely to acknowledge the queued event.
				body, _ := json.Marshal(event.Body)
				fmt.Fprintf(w, `{"Body":%s,"HeadersMap":{"synthetic":["value"]},"EventKey":%q}`, body, event.EventKey)
			}))

			defer server.Close()
			d := testDelivery(t, server.URL, t.TempDir(), 0, 0)
			first, second := durableTestEvent(), durableTestEvent()
			first.Body = strings.Repeat("x", size)
			require.NoError(t, d.Enqueue(context.Background(), first))
			require.NoError(t, d.Enqueue(context.Background(), second))
			d.Start()
			eventuallyEmpty(t, d.directory)
			d.Close()
			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, map[string]int{first.EventKey: 1, second.EventKey: 1}, calls)
		})
	}
}

func TestAcknowledgementRejectsTruncatedObjectsAndTransportErrors(t *testing.T) {
	for _, body := range []string{`{"EventKey":`, `{"EventKey":"key"`, `{42:0}`, `{"Body":[1,{"nested":`, `{"Body":[1,2`} {
		_, _, err := readEventAcknowledgement(strings.NewReader(body))
		require.Error(t, err, "incomplete structured acknowledgement must not release its event: %s", body)
	}
	_, _, err := readEventAcknowledgement(failedAcknowledgementReader{})
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

type failedAcknowledgementReader struct{}

func (failedAcknowledgementReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestDurableLargeAcknowledgementsPreserveIdentityChecks(t *testing.T) {
	for _, identity := range []string{`"wrong"`, `null`, `42`, `{"nested":"value"}`} {
		t.Run(identity, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				fmt.Fprintf(w, `{"Body":%q,"EventKey":%s}`, strings.Repeat("x", 70000), identity)
			}))

			defer server.Close()
			d := testDelivery(t, server.URL, t.TempDir(), 0, 0)
			event := durableTestEvent()
			event.Body = strings.Repeat("x", 70000)
			require.NoError(t, d.Enqueue(context.Background(), event))
			_, complete := d.deliver(pendingFiles(t, d.directory)[0])
			require.False(t, complete)
			require.Len(t, pendingFiles(t, d.directory), 1)
		})
	}
}

func TestAcknowledgementParserNestedDataAndLegacyBodies(t *testing.T) {
	for _, body := range []string{``, `legacy accepted`, `{}`, `[]`, `null`} {
		key, present, err := readEventAcknowledgement(strings.NewReader(body))
		require.NoError(t, err, body)
		require.False(t, present, body)
		require.Empty(t, key, body)
	}

	key, present, err := readEventAcknowledgement(strings.NewReader(`{"Data":[{"EventKey":"nested","Body":"escaped \\\" }"},true,null,5],"EventKey":"actual","Other":[1,2]}`))
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, "actual", key)

	for _, body := range []string{`{"EventKey":"actual","Data":`, `{"EventKey":"actual"}trailing`, `{"EventKey":"first","eventkey":"second"}`, `{"EventKey":null}`} {
		_, _, err := readEventAcknowledgement(strings.NewReader(body))
		require.Error(t, err, body)
	}
}

func TestAcknowledgementEscapingExpansionAndResponseLimit(t *testing.T) {
	var oversized atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event EventDTO
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Error(err)
			return
		}

		data := strings.Repeat(`\u0078`, len(event.Body))
		if oversized.Load() {
			data = strings.Repeat("x", 1000000)
		}

		fmt.Fprintf(w, `{"Body":"%s","EventKey":%q}`, data, event.EventKey)
	}))

	defer server.Close()
	d := testDelivery(t, server.URL, t.TempDir(), 0, 0)
	event := durableTestEvent()
	event.Body = strings.Repeat("x", 70000)
	require.NoError(t, d.Enqueue(context.Background(), event))
	_, complete := d.deliver(pendingFiles(t, d.directory)[0])
	require.True(t, complete)
	// A server may not use an arbitrarily large response to a small request.
	oversized.Store(true)
	require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
	_, complete = d.deliver(pendingFiles(t, d.directory)[0])
	require.False(t, complete)
	require.Len(t, pendingFiles(t, d.directory), 1)
}

type acknowledgementTransportFailure struct{ sent bool }

func (r *acknowledgementTransportFailure) Read(body []byte) (int, error) {
	if r.sent {
		return 0, io.EOF
	}
	r.sent = true
	return copy(body, "legacy accepted"), io.ErrUnexpectedEOF
}

func TestLegacyAcknowledgementRetainsTransportFailure(t *testing.T) {
	_, _, err := readEventAcknowledgement(&acknowledgementTransportFailure{})
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}
