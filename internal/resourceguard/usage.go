// Package resourceguard checks local admission headroom; it does not reserve
// resources against allocations or writes by other processes.
package resourceguard

import "errors"

const MaxUsagePercent = 80

var ErrMemoryPressure = errors.New("memory usage reached 80%; event not accepted")
var ErrFilesystemPressure = errors.New("spool filesystem usage reached 80%")

// reserve rounds up so even small filesystems retain at least twenty percent.
func reserve(total uint64) uint64 {
	return total/5 + min(total%5, 1)
}

func checkMemory(total, available uint64) error {
	if total == 0 || available > total {
		return errors.New("invalid memory capacity measurement")
	}

	if available <= reserve(total) {
		return ErrMemoryPressure
	}

	return nil
}
