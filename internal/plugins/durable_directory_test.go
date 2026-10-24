package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDurableEventsPreservesProvisionedDirectoryPermissions(t *testing.T) {
	directory := t.TempDir()
	mode := os.ModeSetgid | 0775
	require.NoError(t, os.Chmod(directory, mode))
	d := testDelivery(t, "http://synthetic.invalid", directory, 0, 0)
	require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
	info, err := os.Stat(directory)
	require.NoError(t, err)
	require.Equal(t, mode, info.Mode()&(os.ModeSetgid|os.ModePerm))

	for _, path := range append(pendingFiles(t, directory), filepath.Join(directory, ".lock")) {
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}
}

func TestDurableEventsCreatesAndSyncsAncestorsBeforeCapture(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "state", "sensor", "events")
	var synced []string
	syncDir := func(path string) error {
		// Each created entry must be persisted before creating its child.
		for _, child := range []string{"state", "sensor", "events"} {
			if filepath.Base(path) == child {
				require.Contains(t, synced, filepath.Dir(path))
			}
		}

		if path == root {
			require.DirExists(t, filepath.Join(root, "state"))
			require.NoDirExists(t, filepath.Join(root, "state", "sensor"))
		}

		if path == filepath.Join(root, "state") {
			require.NoDirExists(t, directory)
		}

		synced = append(synced, path)
		return syncEventDirectory(path)
	}

	d, err := newDurableEvents("http://synthetic.invalid", "synthetic-token", directory, 0, 0, syncDir)
	require.NoError(t, err)
	t.Cleanup(d.Close)
	for path := directory; filepath.Dir(path) != path; path = filepath.Dir(path) {
		require.Contains(t, synced, filepath.Dir(path))
	}

	for _, path := range []string{filepath.Join(root, "state"), filepath.Join(root, "state", "sensor"), directory} {
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	}

	require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
	require.Equal(t, directory, synced[len(synced)-1], "capture syncs the spool after parent entries were persisted")
}

func TestDurableEventsParentSyncFailurePreventsStartupAndRetryResyncs(t *testing.T) {
	for _, failedLevel := range []string{"state", "sensor", "events"} {
		t.Run(failedLevel, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, "state", "sensor", "events")
			var failedParent string
			syncDir := func(parent string) error {
				if _, err := os.Stat(filepath.Join(parent, failedLevel)); err == nil && failedParent == "" {
					failedParent = parent
					return syscall.EIO
				}
				return syncEventDirectory(parent)
			}

			d, err := newDurableEvents("http://synthetic.invalid", "synthetic-token", directory, 0, 0, syncDir)
			require.ErrorIs(t, err, syscall.EIO)
			require.Nil(t, d, "no capture pipeline can accept events after a failed parent sync")
			require.NotEmpty(t, failedParent)
			require.NoFileExists(t, filepath.Join(directory, ".lock"))

			var synced []string
			d, err = newDurableEvents("http://synthetic.invalid", "synthetic-token", directory, 0, 0, func(path string) error {
				if path == failedParent {
					// The entry left by the failed attempt must still be synced.
					require.DirExists(t, filepath.Join(path, failedLevel))
				}

				synced = append(synced, path)
				return syncEventDirectory(path)
			})

			require.NoError(t, err)
			t.Cleanup(d.Close)
			require.Contains(t, synced, failedParent)
			require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
		})
	}
}

func TestDurableEventsRejectsFileInDirectoryPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(path, nil, 0600))
	for _, directory := range []string{path, filepath.Join(path, "events")} {
		d, err := NewDurableEvents("http://synthetic.invalid", "synthetic-token", directory, 0, 0)
		require.Nil(t, d)
		require.True(t, errors.Is(err, syscall.ENOTDIR), "%v", err)
	}
}
