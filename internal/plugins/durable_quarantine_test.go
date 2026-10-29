package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDurableQuarantineRetriesBothDirectorySyncs(t *testing.T) {
	for _, status := range []int{400, 409, 413, 422, 0} {
		for _, failed := range []string{"quarantine", "source"} {
			name := fmt.Sprintf("%s-%d", failed, status)
			t.Run(name, func(t *testing.T) {
				d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 0, 0)
				first, second := durableTestEvent(), durableTestEvent()
				require.NoError(t, d.Enqueue(context.Background(), first))
				require.NoError(t, d.Enqueue(context.Background(), second))
				path := filepath.Join(d.directory, first.EventKey+".json")
				if status == 0 {
					require.NoError(t, os.WriteFile(path, []byte("synthetic corrupt record"), 0600))
				}

				original, err := os.ReadFile(path)
				require.NoError(t, err)
				var posts, renames int
				rename := d.rename
				d.rename = func(from, to string) error {
					err := rename(from, to)
					if err == nil {
						renames++
					}
					return err
				}

				d.post = func(_ context.Context, payload []byte) (eventPostResult, error) {
					posts++
					var dto EventDTO
					require.NoError(t, json.Unmarshal(payload, &dto))
					if dto.EventKey == first.EventKey {
						return eventPostResult{status: status}, nil
					}

					return eventPostResult{status: 200}, nil
				}

				fail := true
				var synced []string
				d.syncDir = func(dir string) error {
					synced = append(synced, dir)
					if fail && renames > 0 && ((failed == "quarantine" && filepath.Base(dir) == "quarantine") || (failed == "source" && dir == d.directory)) {
						return syscall.EIO
					}

					return syncEventDirectory(dir)
				}

				_, complete := d.deliver(path)
				require.False(t, complete)
				expectedPosts := 1
				if status == 0 {
					expectedPosts = 0
				}

				for range 3 {
					_, complete = d.deliver(path)
					require.False(t, complete, "missing source cannot acknowledge an unsynced quarantine move")
				}

				_, complete = d.deliver(filepath.Join(d.directory, second.EventKey+".json"))
				require.False(t, complete, "replay cannot advance past incomplete quarantine sync")
				require.Equal(t, expectedPosts, posts)
				require.Equal(t, 1, renames, "retry must not rename the event again")
				writeCalls := 0
				write := d.writeFile
				d.writeFile = func(p string, b []byte) error { writeCalls++; return write(p, b) }
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				require.ErrorIs(t, d.Enqueue(ctx, durableTestEvent()), context.Canceled)
				require.Zero(t, writeCalls, "capture must not sync past the incomplete quarantine move")
				files, err := filepath.Glob(filepath.Join(d.directory, "quarantine", "*"))
				require.NoError(t, err)
				require.Len(t, files, 1)
				retained, err := os.ReadFile(files[0])
				require.NoError(t, err)
				require.Equal(t, original, retained)
				fail = false
				synced = nil
				_, complete = d.deliver(path)
				require.True(t, complete)
				require.GreaterOrEqual(t, len(synced), 2)
				require.Equal(t, filepath.Join(d.directory, "quarantine"), synced[0])
				require.Equal(t, d.directory, synced[1])
				_, complete = d.deliver(filepath.Join(d.directory, second.EventKey+".json"))
				require.True(t, complete)
				require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
				require.Equal(t, 1, writeCalls)
			})
		}
	}
}

func TestDurableQuarantineRecoveryRetriesSyncAfterReinitialization(t *testing.T) {
	for _, origin := range []string{"rejected", "corrupt-temporary"} {
		for _, failed := range []string{"quarantine", "source"} {
			t.Run(origin+"-"+failed, func(t *testing.T) {
				directory := t.TempDir()
				qdir := filepath.Join(directory, "quarantine")
				fail := true
				syncDir := func(dir string) error {
					files, _ := filepath.Glob(filepath.Join(qdir, "*"))
					if fail && len(files) > 0 && ((failed == "quarantine" && dir == qdir) || (failed == "source" && dir == directory)) {
						return syscall.EIO
					}

					return syncEventDirectory(dir)
				}

				var original []byte
				if origin == "rejected" {
					d := testDelivery(t, "http://synthetic.invalid", directory, 0, 0)
					event := durableTestEvent()
					require.NoError(t, d.Enqueue(context.Background(), event))
					path := filepath.Join(directory, event.EventKey+".json")
					var err error
					original, err = os.ReadFile(path)
					require.NoError(t, err)
					d.syncDir = syncDir
					d.post = func(context.Context, []byte) (eventPostResult, error) { return eventPostResult{status: 422}, nil }
					_, complete := d.deliver(path)
					require.False(t, complete)
					d.Close()
				} else {
					original = []byte("synthetic incomplete temporary")
					require.NoError(t, os.WriteFile(filepath.Join(directory, ".event-corrupt.tmp"), original, 0600))
				}

				// The failed constructor/previous process has no surviving pending flag.
				for range 2 {
					d, err := newDurableEvents("http://synthetic.invalid", "synthetic-token", directory, 0, 0, syncDir)
					require.ErrorIs(t, err, syscall.EIO)
					require.Nil(t, d)
				}

				files, err := filepath.Glob(filepath.Join(qdir, "*"))
				require.NoError(t, err)
				require.Len(t, files, 1)
				retained, err := os.ReadFile(files[0])
				require.NoError(t, err)
				require.Equal(t, original, retained)
				fail = false
				d, err := newDurableEvents("http://synthetic.invalid", "synthetic-token", directory, 0, 0, syncDir)
				require.NoError(t, err)
				t.Cleanup(d.Close)
				require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
				retained, err = os.ReadFile(files[0])
				require.NoError(t, err)
				require.Equal(t, original, retained)
			})
		}
	}
}

func TestDurableQuarantineSyncFailureBlocksConcurrentCaptureUntilRecovery(t *testing.T) {
	d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 0, 0)
	first, second := durableTestEvent(), durableTestEvent()
	// Fix replay order: durableTestEvent otherwise supplies random v4 keys.
	first.EventKey = "00000000-0000-4000-8000-000000000001"
	second.EventKey = "00000000-0000-4000-8000-000000000002"
	require.NoError(t, d.Enqueue(context.Background(), first))
	require.NoError(t, d.Enqueue(context.Background(), second))
	var fault atomic.Bool
	fault.Store(true)
	var syncAttempts, posts atomic.Int64

	d.syncDir = func(path string) error {
		if filepath.Base(path) == "quarantine" {
			syncAttempts.Add(1)
			if fault.Load() {
				return syscall.EIO
			}
		}

		return syncEventDirectory(path)
	}

	d.post = func(_ context.Context, payload []byte) (eventPostResult, error) {
		var dto EventDTO
		if err := json.Unmarshal(payload, &dto); err != nil {
			return eventPostResult{}, err
		}

		posts.Add(1)
		if dto.EventKey == first.EventKey {
			return eventPostResult{status: 422}, nil
		}

		return eventPostResult{status: 200}, nil
	}

	d.Start()
	require.Eventually(t, func() bool { return syncAttempts.Load() >= 2 }, time.Second, time.Millisecond)
	results, _ := startAdmissionCaptures(t, d, 32, 512)
	require.Eventually(t, func() bool { n, _ := admissionUsage(d); return n == 32 }, time.Second, time.Millisecond)

	select {
	case err := <-results:
		t.Fatalf("capture advanced through failed sync: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	require.Equal(t, int64(1), posts.Load(), "later records cannot pass the incomplete disposition")
	fault.Store(false)
	for range 32 {
		select {
		case err := <-results:
			require.NoError(t, err)
		case <-time.After(3 * time.Second):
			t.Fatal("capture did not recover")
		}
	}

	eventuallyEmpty(t, d.directory)
	require.Equal(t, int64(34), posts.Load(), "rejected record is not posted again, all later events are delivered")
	files, err := filepath.Glob(filepath.Join(d.directory, "quarantine", "*"))
	require.NoError(t, err)
	require.Len(t, files, 1)
}
