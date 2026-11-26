package resourceguard

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemoryUsageBoundary(t *testing.T) {
	for _, tc := range []struct {
		total, available  uint64
		pressure, invalid bool
	}{
		{100, 21, false, false}, {100, 20, true, false}, {100, 19, true, false},
		{100, 0, true, false}, {101, 21, true, false}, {101, 22, false, false},
		{math.MaxUint64, math.MaxUint64 / 4, false, false},
		{0, 0, false, true}, {100, 101, false, true},
	} {
		err := checkMemory(tc.total, tc.available)

		if tc.invalid {
			require.Error(t, err)
			require.NotErrorIs(t, err, ErrMemoryPressure)
		} else if tc.pressure {
			require.ErrorIs(t, err, ErrMemoryPressure)
		} else {
			require.NoError(t, err)
		}
	}
}

func TestFilesystemUsageBoundaryAndAllocationRounding(t *testing.T) {
	for _, tc := range []struct {
		name            string
		free, available uint64
		growth          int64
		pressure        bool
	}{
		{"below threshold", 21, 21, 4096, false},
		{"at threshold", 20, 20, 0, true},
		{"above threshold", 19, 19, 0, true},
		{"projected above threshold", 21, 21, 4097, true},
		{"allocation rounding", 22, 22, 4097, false},
		{"reserved from user", 30, 0, 1, true},
		{"reserved blocks count as unavailable", 30, 20, 0, true},
		{"user availability", 30, 1, 4097, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stats := syscall.Statfs_t{Blocks: 100, Bsize: 4096, Bfree: tc.free}
			// Use the native field type on each Unix platform.
			stats.Bavail = tc.available
			err := checkFilesystem(stats, tc.growth)

			if tc.pressure {
				require.ErrorIs(t, err, ErrFilesystemPressure)
			} else {
				require.NoError(t, err)
			}
		})
	}

	for _, stats := range []syscall.Statfs_t{{}, {Blocks: 100, Bsize: 4096, Bfree: 101}, {Blocks: 100, Bsize: 4096, Bfree: 20, Bavail: 21}, {Blocks: 100, Bfree: 30, Bavail: 30}} {
		err := checkFilesystem(stats, 1)
		require.Error(t, err)
		require.False(t, errors.Is(err, ErrFilesystemPressure))
	}

	require.Error(t, checkFilesystem(syscall.Statfs_t{Blocks: 100, Bsize: 4096, Bfree: 30, Bavail: 30}, -1))
}

func TestFilesystemMeasuresSpoolMount(t *testing.T) {
	directory := t.TempDir()
	var stats syscall.Statfs_t
	require.NoError(t, syscall.Statfs(directory, &stats))
	require.Equal(t, checkFilesystem(stats, 1), FilesystemHasRoom(directory, 1))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "file"), []byte("synthetic"), 0600))
	require.Error(t, FilesystemHasRoom(filepath.Join(directory, "missing"), 1))
}
