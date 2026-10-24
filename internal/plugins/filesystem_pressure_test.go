package plugins

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/resourceguard"
	"github.com/stretchr/testify/require"
)

func TestDurableFilesystemPressureRetainsOriginalAndRecovers(t *testing.T) {
	var pressure atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { body, _ := io.ReadAll(r.Body); w.Write(body) }))
	defer server.Close()
	directory := t.TempDir()
	d := testDelivery(t, server.URL, directory, 0, 0)
	room := d.filesystemRoom
	d.filesystemRoom = func(growth int64) error {
		if pressure.Load() {
			return resourceguard.ErrFilesystemPressure
		}
		return room(growth)
	}

	first := durableTestEvent()
	require.NoError(t, d.Enqueue(context.Background(), first))
	path := pendingFiles(t, directory)[0]
	original, err := os.ReadFile(path)
	require.NoError(t, err)
	pressure.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, d.Enqueue(ctx, durableTestEvent()), context.Canceled)
	require.Len(t, pendingFiles(t, directory), 1)
	retained, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, retained)
	// An already persisted identity can finish acceptance without allocating a
	// new record, and existing delivery remains independent of admission pressure.
	require.NoError(t, d.Enqueue(ctx, first))
	d.Start()
	eventuallyEmpty(t, directory)
	second := durableTestEvent()
	done := make(chan error, 1)

	go func() { done <- d.Enqueue(context.Background(), second) }()
	select {
	case err := <-done:
		t.Fatalf("pressure unexpectedly accepted a new event: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	pressure.Store(false)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("admission did not resume after space recovered")
	}

	eventuallyEmpty(t, directory)
	require.Empty(t, pendingFiles(t, directory))
}

func TestDurableFilesystemMeasurementErrorDoesNotAcceptOrDrop(t *testing.T) {
	directory := t.TempDir()
	d := testDelivery(t, "http://synthetic.invalid", directory, 0, 0)
	room := d.filesystemRoom
	failure := errors.New("synthetic filesystem measurement failure")
	d.filesystemRoom = func(int64) error { return failure }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	event := durableTestEvent()
	require.ErrorIs(t, d.Enqueue(ctx, event), context.Canceled)
	require.Empty(t, pendingFiles(t, directory))
	temporary, err := filepath.Glob(filepath.Join(directory, ".event-*.tmp"))
	require.NoError(t, err)
	require.Empty(t, temporary)
	d.filesystemRoom = room
	require.NoError(t, d.Enqueue(context.Background(), event))
	require.Len(t, pendingFiles(t, directory), 1)
}
