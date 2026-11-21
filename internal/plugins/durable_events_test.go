package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func durableTestEvent() tracer.Event {
	return tracer.Event{EventKey: uuid.NewString(), DateTime: "2026-10-15T10:00:00.123456789Z", ID: "synthetic-session", Protocol: "SSH", Status: "Interaction", Command: "synthetic-command"}
}

func testDelivery(t *testing.T, uri, directory string, maxBytes, maxRecords int64) *DurableEvents {
	t.Helper()
	d, err := NewDurableEvents(uri, "synthetic-token", directory, maxBytes, maxRecords)
	require.NoError(t, err)
	d.retryBase = 5 * time.Millisecond
	d.retryCap = 20 * time.Millisecond
	d.slowRetry = 10 * time.Millisecond
	t.Cleanup(d.Close)
	return d
}

func pendingFiles(t *testing.T, directory string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(directory, "*.json"))
	require.NoError(t, err)
	return paths
}

func eventuallyEmpty(t *testing.T, directory string) {
	t.Helper()
	require.Eventually(t, func() bool { paths, _ := filepath.Glob(filepath.Join(directory, "*.json")); return len(paths) == 0 }, 3*time.Second, 5*time.Millisecond)
}

func TestDurableEventsHTTPFailuresAndRecovery(t *testing.T) {
	for _, status := range []int{500, 502, 503, 504, 408, 429, 401, 403, 404, 201} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			var recoverAPI atomic.Bool
			var mu sync.Mutex
			var bodies [][]byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/events", r.URL.Path)
				require.Equal(t, "synthetic-token", r.Header.Get("Authorization"))
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				bodies = append(bodies, body)
				mu.Unlock()
				calls.Add(1)

				if !recoverAPI.Load() {
					w.WriteHeader(status)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				w.Write(body)
			}))

			defer server.Close()
			directory := t.TempDir()
			d := testDelivery(t, server.URL, directory, 0, 0)
			event := durableTestEvent()
			require.NoError(t, d.Enqueue(context.Background(), event))
			d.Start()
			require.Eventually(t, func() bool { return calls.Load() >= 2 }, time.Second, time.Millisecond)
			require.Len(t, pendingFiles(t, directory), 1)
			recoverAPI.Store(true)
			eventuallyEmpty(t, directory)
			d.Close()
			mu.Lock()
			defer mu.Unlock()
			for _, body := range bodies {
				require.Equal(t, bodies[0], body)
			}

			require.Contains(t, string(bodies[0]), event.EventKey)
		})
	}
}

func TestDurableEventsLostAcknowledgement(t *testing.T) {
	var calls atomic.Int32
	var accepted atomic.Int32
	var original []byte
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		if original == nil {
			original = body
			accepted.Add(1)
		} else if !bytes.Equal(original, body) {
			t.Error("retry changed original event")
		}

		mu.Unlock()
		if calls.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}

			return
		}

		w.Write(body)
	}))

	defer server.Close()
	directory := t.TempDir()
	d := testDelivery(t, server.URL, directory, 0, 0)
	event := durableTestEvent()
	require.NoError(t, d.Enqueue(context.Background(), event))
	d.Start()
	eventuallyEmpty(t, directory)
	require.GreaterOrEqual(t, calls.Load(), int32(2))
	require.Equal(t, int32(1), accepted.Load())
}

func TestDurableEventsAcknowledgementCompatibility(t *testing.T) {
	for _, ack := range []string{"", `{}`, `legacy accepted`, `{"EventKey":"wrong"}`, `{"EventKey":42}`} {
		t.Run(ack, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, ack) }))
			defer server.Close()
			directory := t.TempDir()
			d := testDelivery(t, server.URL, directory, 0, 0)
			require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
			d.Start()

			if strings.Contains(ack, "EventKey") {
				require.Eventually(t, func() bool { return calls.Load() >= 2 }, time.Second, time.Millisecond)
				require.Len(t, pendingFiles(t, directory), 1)
			} else {
				eventuallyEmpty(t, directory)
			}
		})
	}
}

func TestDurableEventsConcurrentCaptureReplay(t *testing.T) {
	var mu sync.Mutex
	keys := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event EventDTO
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Error(err)
			return
		}

		mu.Lock()
		keys[event.EventKey]++
		mu.Unlock()
		json.NewEncoder(w).Encode(event)
	}))

	defer server.Close()
	directory := t.TempDir()
	d := testDelivery(t, server.URL, directory, 0, 0)
	d.Start()
	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			for range 10 {
				if err := d.Enqueue(context.Background(), durableTestEvent()); err != nil {
					t.Error(err)
				}
			}
		})
	}

	wg.Wait()
	eventuallyEmpty(t, directory)
	d.Close()
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, keys, 80)

	for _, count := range keys {
		require.Equal(t, 1, count)
	}
}

func TestDurableEventsPermanentRejectionRetained(t *testing.T) {
	for _, status := range []int{400, 409, 413, 422} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			directory := t.TempDir()
			d := testDelivery(t, server.URL, directory, 0, 0)
			event := durableTestEvent()
			require.NoError(t, d.Enqueue(context.Background(), event))
			d.Start()
			eventuallyEmpty(t, directory)
			paths, err := filepath.Glob(filepath.Join(directory, "quarantine", "*"))
			require.NoError(t, err)
			require.Len(t, paths, 1)
			require.Contains(t, paths[0], fmt.Sprintf("http-%d", status))
			body, err := os.ReadFile(paths[0])
			require.NoError(t, err)
			require.Contains(t, string(body), event.EventKey)
		})
	}
}

func TestDurableEventsCapacityAndCancellation(t *testing.T) {
	directory := t.TempDir()
	d := testDelivery(t, "http://127.0.0.1:1", directory, 0, 1)
	require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, d.Enqueue(ctx, durableTestEvent()), context.DeadlineExceeded)
	require.Len(t, pendingFiles(t, directory), 1)
	result := make(chan error, 1)
	go func() { result <- d.Enqueue(context.Background(), durableTestEvent()) }()
	d.Close()

	select {
	case err := <-result:
		require.ErrorIs(t, err, ErrEventSpoolClosed)
	case <-time.After(time.Second):
		t.Fatal("blocked enqueue leaked after close")
	}

	require.ErrorIs(t, d.Enqueue(context.Background(), durableTestEvent()), ErrEventSpoolClosed)
	small := testDelivery(t, "http://127.0.0.1:1", t.TempDir(), 1, 1)
	require.ErrorIs(t, small.Enqueue(context.Background(), durableTestEvent()), ErrEventTooLarge)
}

func TestDurableEventsStorageFailuresNeverAccepted(t *testing.T) {
	for _, operation := range []string{"write", "rename", "sync"} {
		t.Run(operation, func(t *testing.T) {
			directory := t.TempDir()
			d := testDelivery(t, "http://127.0.0.1:1", directory, 0, 1)
			originalWrite, originalRename, originalSync := d.writeFile, d.rename, d.syncDir

			switch operation {
			case "write":
				d.writeFile = func(path string, b []byte) error { _ = os.WriteFile(path, b[:len(b)/2], 0600); return syscall.ENOSPC }
			case "rename":
				d.rename = func(string, string) error { return syscall.ENOSPC }
			case "sync":
				d.syncDir = func(string) error { return syscall.EIO }
			}

			event := durableTestEvent()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			require.ErrorIs(t, d.Enqueue(ctx, event), context.DeadlineExceeded)
			cancel()
			d.writeFile, d.rename, d.syncDir = originalWrite, originalRename, originalSync
			require.NoError(t, d.Enqueue(context.Background(), event))
			require.Len(t, pendingFiles(t, directory), 1)
			info, err := os.Stat(pendingFiles(t, directory)[0])
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0600), info.Mode().Perm())
		})
	}
}

func TestDurableEventsRecoveryAndOwnership(t *testing.T) {
	directory := t.TempDir()
	d := testDelivery(t, "http://127.0.0.1:1", directory, 0, 0)
	event := durableTestEvent()
	require.NoError(t, d.Enqueue(context.Background(), event))
	paths := pendingFiles(t, directory)
	require.NoError(t, os.Rename(paths[0], filepath.Join(directory, ".event-complete.tmp")))
	require.NoError(t, os.WriteFile(filepath.Join(directory, ".event-broken.tmp"), []byte(`{"truncated":`), 0600))
	_, err := NewDurableEvents("http://127.0.0.1:1", "synthetic-token", directory, 0, 0)
	require.ErrorContains(t, err, "already owned")
	d.Close()
	recovered := testDelivery(t, "http://127.0.0.1:1", directory, 0, 0)
	require.Len(t, pendingFiles(t, directory), 1)
	record, err := recovered.readRecord(pendingFiles(t, directory)[0])
	require.NoError(t, err)
	require.Equal(t, event.EventKey, record.EventKey)
	paths, err = filepath.Glob(filepath.Join(directory, "quarantine", "*"))
	require.NoError(t, err)
	require.Len(t, paths, 1)

	// Quarantine is included in the configured capacity.
	recovered.maxRecords = 2
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, recovered.Enqueue(ctx, durableTestEvent()), context.DeadlineExceeded)
}

func TestDurableEventsReferencePatchRecord(t *testing.T) {
	directory := t.TempDir()
	event := durableTestEvent()
	event.CommandRaw = `\xff`
	body, err := json.Marshal(event)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "reference.json"), body, 0600))
	d := testDelivery(t, "http://127.0.0.1:1", directory, 0, 0)

	d.post = func(_ context.Context, payload []byte) (eventPostResult, error) {
		var dto EventDTO
		require.NoError(t, json.Unmarshal(payload, &dto))
		require.Equal(t, event.EventKey, dto.EventKey)
		require.Equal(t, event.DateTime, dto.DateTime)
		require.NotContains(t, string(payload), "CommandRaw")
		return eventPostResult{status: 200, keyPresent: true, key: event.EventKey}, nil
	}

	d.Start()
	eventuallyEmpty(t, directory)
}

func TestDurableEventsRequestsAndBackoffCancel(t *testing.T) {
	for _, duringRequest := range []bool{false, true} {
		t.Run(fmt.Sprint(duringRequest), func(t *testing.T) {
			entered := make(chan struct{})
			d := testDelivery(t, "http://127.0.0.1:1", t.TempDir(), 0, 0)
			d.retryBase = time.Hour

			d.post = func(ctx context.Context, _ []byte) (eventPostResult, error) {
				close(entered)
				if duringRequest {
					<-ctx.Done()
				}

				return eventPostResult{}, errors.New("synthetic timeout")
			}

			require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
			d.Start()
			<-entered
			done := make(chan struct{})
			go func() { d.Close(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("shutdown did not cancel request/backoff")
			}
		})
	}
}

func TestDurableEventsTokenChangeRetainsRecords(t *testing.T) {
	directory := t.TempDir()
	d := testDelivery(t, "http://127.0.0.1:1", directory, 0, 0)
	require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
	d.Close()
	changed, err := NewDurableEvents("http://127.0.0.1:1", "replacement-token", directory, 0, 0)
	require.NoError(t, err)
	defer changed.Close()

	changed.post = func(context.Context, []byte) (eventPostResult, error) {
		t.Error("sent old event under a changed token")
		return eventPostResult{status: 200}, nil
	}

	_, done := changed.deliver(pendingFiles(t, directory)[0])
	require.False(t, done)
	require.Len(t, pendingFiles(t, directory), 1)
}

func TestDurableEventsRetryAfter(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for value, want := range map[string]time.Duration{"2": 2 * time.Second, "-1": 0, "invalid": 0, "999999999999": 15 * time.Minute, now.Add(5 * time.Second).Format(http.TimeFormat): 5 * time.Second, now.Add(-time.Minute).Format(http.TimeFormat): 0} {
		require.Equal(t, want, eventRetryAfter(value, now), value)
	}
}

func TestDurableEventsSIGKILL(t *testing.T) {
	if directory := os.Getenv("BEELZEBUB_TEST_CRASH_SPOOL"); directory != "" {
		d, err := NewDurableEvents("http://127.0.0.1:1", "synthetic-token", directory, 0, 0)
		if err != nil {
			os.Exit(2)
		}

		if d.Enqueue(context.Background(), durableTestEvent()) != nil {
			os.Exit(3)
		}

		fmt.Println("durably captured")
		select {}
	}

	directory := t.TempDir()
	executable, err := os.Executable()
	require.NoError(t, err)
	command := exec.Command(executable, "-test.run=^TestDurableEventsSIGKILL$")
	command.Env = append(os.Environ(), "BEELZEBUB_TEST_CRASH_SPOOL="+directory)
	output, err := command.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, command.Start())
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	ready := make(chan []byte, 1)

	go func() {
		line := make([]byte, len("durably captured\n"))
		_, _ = io.ReadFull(output, line)
		ready <- line
	}()

	select {
	case line := <-ready:
		require.Equal(t, "durably captured\n", string(line))
	case <-time.After(10 * time.Second):
		t.Fatal("child did not capture event")
	}

	before, err := os.ReadFile(pendingFiles(t, directory)[0])
	require.NoError(t, err)
	require.NoError(t, command.Process.Kill())
	require.Error(t, command.Wait())
	d := testDelivery(t, "http://127.0.0.1:1", directory, 0, 0)
	d.post = func(_ context.Context, payload []byte) (eventPostResult, error) {
		var record spoolRecord
		require.NoError(t, json.Unmarshal(before, &record))
		require.Equal(t, []byte(record.Payload), payload)
		return eventPostResult{status: 200, keyPresent: true, key: record.EventKey}, nil
	}

	d.Start()
	eventuallyEmpty(t, directory)
}

func TestDurableEventsDeleteFailureReplaysSameIdentity(t *testing.T) {
	directory := t.TempDir()
	d := testDelivery(t, "http://127.0.0.1:1", directory, 0, 0)
	event := durableTestEvent()
	require.NoError(t, d.Enqueue(context.Background(), event))
	var calls int

	d.post = func(_ context.Context, body []byte) (eventPostResult, error) {
		calls++
		return eventPostResult{status: 200, keyPresent: true, key: event.EventKey}, nil
	}

	remove := d.remove
	d.remove = func(string) error { return syscall.EIO }
	path := pendingFiles(t, directory)[0]
	_, complete := d.deliver(path)
	require.False(t, complete)
	require.FileExists(t, path)
	d.remove = remove
	_, complete = d.deliver(path)
	require.True(t, complete)
	require.Equal(t, 2, calls)
}

func TestDurableEventsActualRequestCancellation(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; <-release }))
	defer server.Close()
	defer close(release)
	d := testDelivery(t, server.URL, t.TempDir(), 0, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := d.post(ctx, []byte(`{}`))
	require.Error(t, err)
	<-entered
}

func TestDurableEventsByteBudgetAndStorageRecovery(t *testing.T) {
	directory := t.TempDir()
	d := testDelivery(t, "http://127.0.0.1:1", directory, 0, 0)
	require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
	first := pendingFiles(t, directory)[0]
	info, err := os.Stat(first)
	require.NoError(t, err)
	d.maxBytes = info.Size() + 1
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, d.Enqueue(ctx, durableTestEvent()), context.DeadlineExceeded)
	require.NoError(t, os.Remove(first))
	require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
	require.Len(t, pendingFiles(t, directory), 1)
}

func TestDurableEventsHTTPTimeoutRetriesOriginalEvent(t *testing.T) {
	var calls atomic.Int32
	var mu sync.Mutex
	var first []byte
	release := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if calls.Add(1) == 1 {
			mu.Lock()
			first = body
			mu.Unlock()
			<-release
			return
		}

		mu.Lock()
		require.Equal(t, first, body)
		mu.Unlock()
		w.Write(body)
	}))

	defer server.Close()
	defer close(release)
	directory := t.TempDir()
	d := testDelivery(t, server.URL, directory, 0, 0)
	post := d.post

	d.post = func(ctx context.Context, body []byte) (eventPostResult, error) {
		requestCtx, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
		defer cancel()
		return post(requestCtx, body)
	}

	require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
	d.Start()
	eventuallyEmpty(t, directory)
	require.GreaterOrEqual(t, calls.Load(), int32(2))
}
