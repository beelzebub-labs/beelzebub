package resourceguard

import (
	"errors"
	"fmt"
	"syscall"
)

// FilesystemHasRoom checks the spool's filesystem, including other files and
// space reserved from this user. Account in allocation blocks, not payload bytes.
func FilesystemHasRoom(directory string, growth int64) error {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(directory, &stats); err != nil {
		return fmt.Errorf("measure spool filesystem capacity: %w", err)
	}

	return checkFilesystem(stats, growth)
}

func checkFilesystem(stats syscall.Statfs_t, growth int64) error {
	total, free, available := stats.Blocks, stats.Bfree, uint64(stats.Bavail)
	if total == 0 || free > total || available > free || stats.Bsize <= 0 || growth < 0 {
		return errors.New("invalid spool filesystem capacity measurement")
	}

	minimum := reserve(total)
	// Reserved blocks that this sensor cannot use are not operational headroom.
	free = min(free, available)
	if free <= minimum {
		return ErrFilesystemPressure
	}

	blockSize := uint64(stats.Bsize)
	blocks := uint64(growth) / blockSize
	if uint64(growth)%blockSize != 0 {
		blocks++
	}

	if blocks > free-minimum {
		return ErrFilesystemPressure
	}

	return nil
}
