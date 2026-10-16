package plugins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func assertSpoolAccounting(t *testing.T, d *DurableEvents) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	size, count, err := d.diskUsage()
	require.NoError(t, err)
	require.True(t, d.usageValid)
	require.Equal(t, size, d.usedBytes, "byte accounting must match retained files")
	require.Equal(t, count, d.usedRecords, "record accounting must match retained files")
}

func TestDurableAccountingTracksPartialWritesAndRetries(t *testing.T) {
	for _, failure := range []string{"partial", "rename", "directory-sync"} {
		t.Run(failure, func(t *testing.T) {
			d := testDelivery(t, "http://127.0.0.1:1", t.TempDir(), 0, 2)
			write, rename, syncDir := d.writeFile, d.rename, d.syncDir

			switch failure {
			case "partial":
				d.writeFile = func(path string, body []byte) error {
					if err := os.WriteFile(path, body[:len(body)/2], 0600); err != nil {
						return err
					}
					return syscall.ENOSPC
				}
			case "rename":
				d.rename = func(string, string) error { return syscall.EIO }
			case "directory-sync":
				d.syncDir = func(string) error { return syscall.EIO }
			}

			event := durableTestEvent()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			require.ErrorIs(t, d.Enqueue(ctx, event), context.DeadlineExceeded)
			cancel()
			assertSpoolAccounting(t, d)
			require.Equal(t, int64(1), d.usedRecords)
			d.writeFile, d.rename, d.syncDir = write, rename, syncDir
			require.NoError(t, d.Enqueue(context.Background(), event))
			assertSpoolAccounting(t, d)
			require.Equal(t, int64(1), d.usedRecords)
			d.post = func(context.Context, []byte) (eventPostResult, error) { return eventPostResult{status: 200}, nil }
			_, complete := d.deliver(pendingFiles(t, d.directory)[0])
			require.True(t, complete)
			assertSpoolAccounting(t, d)
			require.Zero(t, d.usedRecords)
			require.Zero(t, d.usedBytes)
		})
	}
}

func TestDurableAccountingRetainsQuarantineAndFailedRemoval(t *testing.T) {
	d := testDelivery(t, "http://127.0.0.1:1", t.TempDir(), 0, 2)
	require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
	first := pendingFiles(t, d.directory)[0]
	d.post = func(context.Context, []byte) (eventPostResult, error) { return eventPostResult{status: 400}, nil }
	_, complete := d.deliver(first)
	require.True(t, complete)
	assertSpoolAccounting(t, d)
	require.Equal(t, int64(1), d.usedRecords)
	require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
	d.post = func(context.Context, []byte) (eventPostResult, error) { return eventPostResult{status: 200}, nil }
	remove := d.remove
	d.remove = func(string) error { return syscall.EIO }
	path := pendingFiles(t, d.directory)[0]
	_, complete = d.deliver(path)
	require.False(t, complete)
	assertSpoolAccounting(t, d)
	require.Equal(t, int64(2), d.usedRecords)
	d.remove = remove
	_, complete = d.deliver(path)
	require.True(t, complete)
	assertSpoolAccounting(t, d)
	require.Equal(t, int64(1), d.usedRecords)
}

func TestDurableAccountingInitializesRetainedBacklog(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(directory, "quarantine"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "old.json"), []byte("pending"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "quarantine", "old.rejected"), []byte("rejected"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, ".unknown.tmp"), []byte("temporary"), 0600))
	d := testDelivery(t, "http://127.0.0.1:1", directory, 0, 4)
	require.Equal(t, int64(3), d.usedRecords)
	assertSpoolAccounting(t, d)
	require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
	assertSpoolAccounting(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, d.Enqueue(ctx, durableTestEvent()), context.DeadlineExceeded)
}

func TestDurableEnqueueNeverScansRetainedFiles(t *testing.T) {
	directory := t.TempDir()
	quarantine := filepath.Join(directory, "quarantine")
	require.NoError(t, os.Mkdir(quarantine, 0700))
	for i := range 3000 {
		require.NoError(t, os.WriteFile(filepath.Join(quarantine, fmt.Sprint(i)), []byte("rejected"), 0600))
	}

	d := testDelivery(t, "http://127.0.0.1:1", directory, 0, 0)
	var scans atomic.Int32
	scan := d.scanUsage
	d.scanUsage = func() (int64, int64, error) { scans.Add(1); return scan() }
	for range 30 {
		require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
	}

	require.Zero(t, scans.Load(), "capture must not enumerate the backlog or quarantine")
	assertSpoolAccounting(t, d)
}

func TestDurableReconciliationDoesNotBlockOrOverwriteCapture(t *testing.T) {
	d := testDelivery(t, "http://127.0.0.1:1", t.TempDir(), 0, 0)
	require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
	started := make(chan struct{})
	release := make(chan struct{})
	scan := d.scanUsage
	d.scanUsage = func() (int64, int64, error) {
		size, count, err := scan()
		close(started)
		<-release
		return size, count, err
	}

	scanned := make(chan struct{})
	go func() { d.reconcileUsage(); close(scanned) }()
	<-started
	// Keep the scheduled reconciler out of this controlled scan; Close starts it
	// only after cancellation. The wake is still recorded for the newer snapshot.
	d.reconcileOnce.Do(func() { close(d.reconcileDone) })
	captured := make(chan error, 1)
	go func() { captured <- d.Enqueue(context.Background(), durableTestEvent()) }()
	select {
	case err := <-captured:
		require.NoError(t, err)
	case <-time.After(time.Second):
		close(release)
		t.Fatal("background scan blocked capture")
	}

	close(release)
	<-scanned
	require.Equal(t, int64(2), d.usedRecords, "stale reconciliation overwrote newer capture")
	assertSpoolAccounting(t, d)
}

func TestDurableCapacityReconcilesOperatorCleanup(t *testing.T) {
	directory := t.TempDir()
	d := testDelivery(t, "http://127.0.0.1:1", directory, 0, 1)
	require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
	require.NoError(t, os.Remove(pendingFiles(t, directory)[0]))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, d.Enqueue(ctx, durableTestEvent()))
	// Stop background work before comparing to the authoritative filesystem.
	d.Close()

	size, count, err := func() (int64, int64, error) {
		// diskUsage is cancellation-aware; inspect ordinary file metadata here.
		files, err := filepath.Glob(filepath.Join(directory, "*.json"))
		if err != nil {
			return 0, 0, err
		}
		var size int64
		for _, p := range files {
			info, err := os.Stat(p)
			if err != nil {
				return 0, 0, err
			}
			size += info.Size()
		}
		return size, int64(len(files)), nil
	}()

	require.NoError(t, err)
	require.Equal(t, size, d.usedBytes)
	require.Equal(t, count, d.usedRecords)
}

func BenchmarkDurableEnqueueBacklog(b *testing.B) {
	for _, retained := range []int{0, 30000} {
		b.Run(fmt.Sprintf("retained-%d", retained), func(b *testing.B) {
			directory := b.TempDir()
			quarantine := filepath.Join(directory, "quarantine")
			if err := os.Mkdir(quarantine, 0700); err != nil {
				b.Fatal(err)
			}

			for i := range retained {
				if err := os.WriteFile(filepath.Join(quarantine, fmt.Sprint(i)), []byte("rejected"), 0600); err != nil {
					b.Fatal(err)
				}
			}

			d, err := NewDurableEvents("http://127.0.0.1:1", "synthetic-token", directory, 0, 0)
			if err != nil {
				b.Fatal(err)
			}

			b.Cleanup(d.Close)
			b.ResetTimer()
			for range b.N {
				if err := d.Enqueue(context.Background(), durableTestEvent()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestAcceptedEventOutlivesProducerCancellation(t *testing.T) {
	d := testDelivery(t, "http://127.0.0.1:1", t.TempDir(), 0, 0)
	event := durableTestEvent()
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, d.Enqueue(ctx, event))
	cancel()
	delivered := make(chan struct{}, 1)
	d.post = func(ctx context.Context, _ []byte) (eventPostResult, error) {
		require.NoError(t, ctx.Err())
		delivered <- struct{}{}
		return eventPostResult{status: 200, keyPresent: true, key: event.EventKey}, nil
	}

	d.Start()
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("producer cancellation stopped accepted event delivery")
	}

	eventuallyEmpty(t, d.directory)
}
